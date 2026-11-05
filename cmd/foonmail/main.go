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
	"gioui.org/x/explorer"
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
		if errors.Is(err, errAlreadyRunning) {
			fmt.Println("foonmail is already running; its window was raised")
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "foonmail:", err)
		os.Exit(1)
	}
	app.ID = "foonmail" // Wayland app_id, for Hyprland window rules
	go func() {
		defer cleanup()
		w := new(app.Window)
		w.Option(app.Title("foonmail"), app.Size(unit.Dp(1000), unit.Dp(720)))
		u.w = w
		u.expl = explorer.NewExplorer(w)
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
	db, err := cache.OpenReadOnly(filepath.Join(cfg.CacheDir, "mail.db"))
	if err != nil {
		return nil, nil, err
	}
	// Every account the daemon has cached; one that isn't there yet (just
	// added, or never connected) is left out with a warning.
	ctx := context.Background()
	u := newUI(db, log)
	u.dataDir = cfg.DataDir
	var first *account
	for i := range cfg.Accounts {
		a, err := loadAccount(ctx, db, &cfg.Accounts[i], cfg.DataDir)
		if err != nil {
			log.Warn("account not shown yet", "account", cfg.Accounts[i].Name, "err", err)
			u.missing = append(u.missing, &cfg.Accounts[i])
			continue
		}
		u.accounts = append(u.accounts, a)
		if first == nil || a.cfg.Name == accountName {
			first = a
		}
	}
	switch {
	case first == nil:
		db.Close()
		return nil, nil, errors.New("no account is in the cache yet; is foonmaild running?")
	case accountName != "" && first.cfg.Name != accountName:
		db.Close()
		return nil, nil, fmt.Errorf("account %s isn't in the cache yet; is foonmaild running?", accountName)
	}
	u.useAccount(first)
	u.folder = first.inbox

	cleanup := func() { db.Close() }
	if conn, err := dbus.ConnectSessionBus(); err != nil {
		log.Warn("no session bus: showing cached mail only", "err", err)
	} else {
		client := rpc.NewClient(conn)
		// Keep the client even if the daemon is down now: refreshStatus notices
		// when it comes up, from its StatusChanged signals.
		u.daemon = client
		if err := exportGUI(conn, u); errors.Is(err, errAlreadyRunning) {
			// One window is enough: raise the running one instead.
			ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
			rpc.NewGUIClient(conn).Show(ctx2)
			cancel()
			conn.Close()
			db.Close()
			return nil, nil, errAlreadyRunning
		} else if err != nil {
			log.Warn("the GUI interface isn't available: notifications can't open messages", "err", err)
		}
		// Subscribe even if the daemon is down now; its signals arrive once it starts.
		events, stop, err := client.Subscribe()
		if err == nil {
			go u.follow(events)
			cleanup = func() { stop(); conn.Close(); db.Close() }
		}
	}
	u.reload()
	u.refreshStatus()
	u.restoreUnsent()
	return u, cleanup, nil
}

func newUI(db *cache.DB, log *slog.Logger) *ui {
	u := &ui{
		th:          material.NewTheme(),
		db:          db,
		log:         log,
		now:         time.Now,
		listTag:     new(int),
		viewPtr:     new(int),
		mono:        font.Font{Typeface: "monospace"},
		results:     make(chan func(), 256),
		openURL:     xdgOpen,
		downloads:   downloadDir,
		editorArgv:  defaultEditor,
		focusWindow: hyprlandFocus,
	}
	u.list.Axis = layout.Vertical
	u.pane.tag = new(int)
	u.pane.list.Axis = layout.Vertical
	u.th.TextSize = 15
	u.search.SingleLine, u.search.Submit = true, true
	u.focusReq = u.listTag
	if desktop.PrefersDark() {
		u.th.Palette = material.Palette{Bg: rgb(0x1d1f21), Fg: rgb(0xe0e0e0), ContrastBg: rgb(0x6ea8fe), ContrastFg: rgb(0x1d1f21)}
		u.muted, u.line, u.barBg, u.accent = rgb(0x9a9a9a), rgb(0x3a3d40), rgb(0x25282b), rgb(0x6ea8fe)
		u.selBg, u.selBgBlurred = rgba(0x6ea8fe, 0x50), rgba(0x6ea8fe, 0x28)
		u.quoteColors = []color.NRGBA{rgb(0x8fbf7f), rgb(0xd7a65f), rgb(0xb294bb)}
	} else {
		u.th.Palette = material.Palette{Bg: rgb(0xfafafa), Fg: rgb(0x1f1f1f), ContrastBg: rgb(0x2f6fde), ContrastFg: rgb(0xffffff)}
		u.muted, u.line, u.barBg, u.accent = rgb(0x6b6b6b), rgb(0xd8d8d8), rgb(0xf0f0f0), rgb(0x2f6fde)
		u.selBg, u.selBgBlurred = rgba(0x2f6fde, 0x38), rgba(0x2f6fde, 0x1c)
		u.quoteColors = []color.NRGBA{rgb(0x3d7a2e), rgb(0x9a6200), rgb(0x7b4f9e)}
	}
	return u
}

// follow applies the daemon's signals: reload the folder when it changes,
// refresh the status line when that changes.
func (u *ui) follow(events <-chan rpc.Event) {
	for ev := range events {
		switch ev := ev.(type) {
		case rpc.FolderChanged:
			id := ev.FolderID
			u.post(func() {
				if u.pane.open {
					sel := u.pane.sel
					u.refreshPaneRows()
					u.pane.sel = min(sel, len(u.pane.rows)-1)
				}
				if id == u.folder.ID || (u.sent != nil && id == u.sent.ID) {
					u.reload()
				}
			})
		case rpc.StatusChanged:
			u.refreshStatus()
		}
	}
}

func run(w *app.Window, u *ui) error {
	var ops op.Ops
	focused := false
	for {
		ev := w.Event()
		if u.expl != nil {
			u.expl.ListenEvents(ev)
		}
		switch e := ev.(type) {
		case app.DestroyEvent:
			u.autosave() // what was typed since the last autosave
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
