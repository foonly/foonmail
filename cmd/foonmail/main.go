// Command foonmail is the foonmail mail reader. It reads the cache that
// foonmaild keeps and asks the daemon over D-Bus for anything that needs
// the server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"github.com/godbus/dbus/v5"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/desktop"
	"foonmail/internal/rpc"
)

func main() {
	defaultPath, _ := config.Path()
	configPath := flag.String("config", defaultPath, "configuration file")
	accountName := flag.String("account", "", "account to show (default: the first)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	u, cleanup, err := setup(*configPath, *accountName, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "foonmail:", err)
		os.Exit(1)
	}
	app.ID = "foonmail" // Wayland app_id, for Hyprland window rules
	go func() {
		defer cleanup()
		w := new(app.Window)
		w.Option(app.Title("foonmail"), app.Size(unit.Dp(1000), unit.Dp(720)))
		u.w = w
		if err := run(w, u); err != nil {
			fmt.Fprintln(os.Stderr, "foonmail:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func setup(configPath, accountName string, log *slog.Logger) (*ui, func(), error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	acct := &cfg.Accounts[0]
	if accountName != "" {
		if acct, err = cfg.Account(accountName); err != nil {
			return nil, nil, err
		}
	}
	db, err := cache.OpenReadOnly(filepath.Join(cfg.CacheDir, "mail.db"))
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	accountID, err := db.AccountID(ctx, acct.Name)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("account %s isn't in the cache yet; is foonmaild running? (%v)", acct.Name, err)
	}
	folders, err := db.Folders(ctx, accountID)
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	u := newUI(db, log)
	u.account, u.accountID = acct, accountID
	found := false
	for _, f := range folders {
		if f.Name == "INBOX" {
			u.folder, found = f, true
		}
	}
	if !found {
		db.Close()
		return nil, nil, errors.New("INBOX isn't in the cache yet; is foonmaild running?")
	}

	cleanup := func() { db.Close() }
	if conn, err := dbus.ConnectSessionBus(); err != nil {
		log.Warn("no session bus: showing cached mail only", "err", err)
	} else {
		client := rpc.NewClient(conn)
		// Keep the client even if the daemon is down now: refreshStatus notices
		// when it comes up, from its StatusChanged signals.
		u.daemon = client
		// Subscribe even if the daemon is down now; its signals arrive once it starts.
		events, stop, err := client.Subscribe()
		if err == nil {
			go u.follow(events)
			cleanup = func() { stop(); conn.Close(); db.Close() }
		}
	}
	u.reload()
	u.refreshStatus()
	return u, cleanup, nil
}

func newUI(db *cache.DB, log *slog.Logger) *ui {
	u := &ui{
		th:      material.NewTheme(),
		db:      db,
		log:     log,
		now:     time.Now,
		listTag: new(int),
		mono:    font.Font{Typeface: "monospace"},
		results: make(chan func(), 256),
		openURL: xdgOpen,
	}
	u.list.Axis = layout.Vertical
	u.th.TextSize = 15
	u.search.SingleLine, u.search.Submit = true, true
	u.focusReq = u.listTag
	if desktop.PrefersDark() {
		u.th.Palette = material.Palette{Bg: rgb(0x1d1f21), Fg: rgb(0xe0e0e0), ContrastBg: rgb(0x6ea8fe), ContrastFg: rgb(0x1d1f21)}
		u.muted, u.line, u.barBg, u.accent = rgb(0x9a9a9a), rgb(0x3a3d40), rgb(0x25282b), rgb(0x6ea8fe)
		u.selBg, u.selBgBlurred = rgba(0x6ea8fe, 0x50), rgba(0x6ea8fe, 0x28)
	} else {
		u.th.Palette = material.Palette{Bg: rgb(0xfafafa), Fg: rgb(0x1f1f1f), ContrastBg: rgb(0x2f6fde), ContrastFg: rgb(0xffffff)}
		u.muted, u.line, u.barBg, u.accent = rgb(0x6b6b6b), rgb(0xd8d8d8), rgb(0xf0f0f0), rgb(0x2f6fde)
		u.selBg, u.selBgBlurred = rgba(0x2f6fde, 0x38), rgba(0x2f6fde, 0x1c)
	}
	return u
}

// follow applies the daemon's signals: reload the folder when it changes,
// refresh the status line when that changes.
func (u *ui) follow(events <-chan rpc.Event) {
	for ev := range events {
		switch ev := ev.(type) {
		case rpc.FolderChanged:
			if ev.FolderID == u.folder.ID {
				u.post(u.reload)
			}
		case rpc.StatusChanged:
			u.refreshStatus()
		}
	}
}

func run(w *app.Window, u *ui) error {
	var ops op.Ops
	focused := false
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.ConfigEvent:
			// Looking at the window counts as looking at the mail.
			if e.Config.Focused && !focused {
				u.acknowledge()
			}
			focused = e.Config.Focused
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.drain()
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

func rgb(c uint32) color.NRGBA { return rgba(c, 0xff) }

func rgba(c uint32, a uint8) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: a}
}
