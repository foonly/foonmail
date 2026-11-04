package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/render"
	"foonmail/internal/rpc"
)

// daemon is what the GUI needs from foonmaild (rpc.Client), so tests can
// use a fake. It is nil when the daemon isn't reachable.
type daemon interface {
	FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error)
	SetFlags(ctx context.Context, folderID int64, uids []uint32, add, remove []string) error
	Acknowledge(ctx context.Context) error
	Status(ctx context.Context) ([]rpc.AccountStatus, error)
}

type mode int

const (
	modeList mode = iota
	modeView
)

// message is the open message.
type message struct {
	sum        cache.Summary
	loading    bool
	err        error
	rendered   *render.Message
	showQuotes bool
	linkInput  string // digits typed to open a link

	body widget.Selectable
	list widget.List
}

type ui struct {
	w      *app.Window
	th     *material.Theme
	db     *cache.DB
	daemon daemon
	log    *slog.Logger
	now    func() time.Time

	account   *config.Account
	accountID int64
	folder    cache.Folder

	items []cache.Summary // the folder, newest first
	view  []int           // indices into items, after filtering
	sel   int             // index into view
	mode  mode

	list    widget.List
	listTag *int
	click   gesture.Click
	rowH    int
	viewH   int

	search    widget.Editor
	searching bool

	msg *message

	status   *rpc.AccountStatus // nil until known
	down     bool               // the daemon didn't answer the last Status call
	openURL  func(string) error // opens a link; xdg-open
	notice   string             // transient message in the status bar
	focusReq event.Tag

	results chan func()

	mono                       font.Font
	muted, line, barBg, accent color.NRGBA
	selBg, selBgBlurred        color.NRGBA
}

// post runs f on the UI goroutine and wakes the window.
func (u *ui) post(f func()) {
	u.results <- f
	if u.w != nil {
		u.w.Invalidate()
	}
}

func (u *ui) drain() {
	for {
		select {
		case f := <-u.results:
			f()
		default:
			return
		}
	}
}

// reload rereads the folder from the cache, keeping the selection.
func (u *ui) reload() {
	keep := uint32(0)
	if s := u.current(); s != nil {
		keep = s.UID
	}
	items, err := u.db.List(context.Background(), u.folder.ID, 1_000_000, 0)
	if err != nil {
		u.notice = "Reading the cache failed: " + err.Error()
		return
	}
	u.items = items
	u.applyFilter(keep)
}

// applyFilter rebuilds view from the search field, keeping the message
// with UID keep selected if it is still shown.
func (u *ui) applyFilter(keep uint32) {
	u.view = u.view[:0]
	words := strings.Fields(u.search.Text())
	if !u.searching || len(words) == 0 {
		for i := range u.items {
			u.view = append(u.view, i)
		}
	} else {
		hits, err := u.db.Search(context.Background(), u.accountID, u.folder.ID, words, 5000)
		if err != nil {
			u.notice = "Search failed: " + err.Error()
		}
		match := make(map[uint32]bool, len(hits))
		for _, h := range hits {
			match[h.UID] = true
		}
		for i, it := range u.items {
			if match[it.UID] {
				u.view = append(u.view, i)
			}
		}
	}
	u.sel = 0
	for vi, i := range u.view {
		if u.items[i].UID == keep {
			u.sel = vi
			break
		}
	}
	u.ensureVisible()
}

func (u *ui) current() *cache.Summary {
	if u.sel < 0 || u.sel >= len(u.view) {
		return nil
	}
	return &u.items[u.view[u.sel]]
}

func (u *ui) moveTo(i int) {
	if len(u.view) == 0 {
		return
	}
	u.sel = min(max(i, 0), len(u.view)-1)
	u.ensureVisible()
}

func (u *ui) ensureVisible() {
	if u.rowH == 0 || u.viewH == 0 {
		return
	}
	visible := max(1, u.viewH/u.rowH)
	p := &u.list.Position
	switch {
	case u.sel <= p.First:
		p.First, p.Offset = u.sel, 0
	case u.sel >= p.First+visible:
		p.First, p.Offset = u.sel-visible+1, 0
	}
}

func (u *ui) rowAt(y int) int {
	if u.rowH == 0 {
		return -1
	}
	i := u.list.Position.First + (y+u.list.Position.Offset)/u.rowH
	if i < 0 || i >= len(u.view) {
		return -1
	}
	return i
}

// open shows the selected message, fetching and rendering it in the
// background, and marks it read.
func (u *ui) open() {
	s := u.current()
	if s == nil {
		return
	}
	m := &message{sum: *s, loading: true}
	m.list.Axis = layout.Vertical
	u.msg, u.mode = m, modeView
	sum := *s
	go func() {
		raw, err := u.loadRaw(sum)
		var r *render.Message
		if err == nil {
			r, err = render.Render(raw, render.Options{})
		}
		u.post(func() {
			if err == nil && unread(sum) {
				u.setSeen(sum.UID, true)
			}
			if u.msg != m {
				return // the user moved on
			}
			m.loading, m.rendered, m.err = false, r, err
		})
	}()
}

// loadRaw returns a message's raw content: from the cache if it was
// fetched before, otherwise through the daemon.
func (u *ui) loadRaw(s cache.Summary) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if raw, err := u.db.Raw(ctx, s.ID); err == nil && raw != nil {
		return raw, nil
	}
	if u.daemon == nil {
		return nil, errors.New("foonmaild isn't running, and this message hasn't been downloaded yet")
	}
	id, err := u.daemon.FetchBody(ctx, s.FolderID, s.UID)
	if errors.Is(err, rpc.ErrNotRunning) {
		return nil, errors.New("foonmaild isn't running, and this message hasn't been downloaded yet")
	}
	if errors.Is(err, rpc.ErrOffline) {
		return nil, errors.New("offline, and this message hasn't been downloaded yet")
	}
	if err != nil {
		return nil, err
	}
	raw, err := u.db.Raw(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && raw == nil) {
		return nil, errors.New("the message disappeared from the cache")
	}
	return raw, err
}

// setSeen marks a message read or unread: at once in the list, then on the
// server through the daemon, undoing the change if that fails. It runs on
// the UI goroutine.
func (u *ui) setSeen(uid uint32, seen bool) {
	apply := func(on bool) {
		for i := range u.items {
			if u.items[i].UID == uid {
				u.items[i].Flags = setFlag(u.items[i].Flags, `\Seen`, on)
			}
		}
		if u.msg != nil && u.msg.sum.UID == uid {
			u.msg.sum.Flags = setFlag(u.msg.sum.Flags, `\Seen`, on)
		}
	}
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: can't change flags"
		return
	}
	apply(seen)
	go func() {
		var add, remove []string
		if seen {
			add = []string{`\Seen`}
		} else {
			remove = []string{`\Seen`}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := u.daemon.SetFlags(ctx, u.folder.ID, []uint32{uid}, add, remove); err != nil {
			u.post(func() { apply(!seen); u.notice = "Couldn't change the flag: " + err.Error() })
		}
	}()
}

// refreshStatus asks the daemon for the account's state.
func (u *ui) refreshStatus() {
	if u.daemon == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := u.daemon.Status(ctx)
		u.post(func() {
			u.down = err != nil
			if err != nil {
				u.status = nil
				return
			}
			for i := range st {
				if st[i].Name == u.account.Name {
					u.status = &st[i]
				}
			}
		})
	}()
}

// acknowledge tells the daemon the user is looking, which clears the tray.
func (u *ui) acknowledge() {
	if u.daemon == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := u.daemon.Acknowledge(ctx); err != nil {
			u.log.Debug("acknowledge", "err", err)
		}
	}()
}

// openLink opens a numbered link with the desktop's handler.
func (u *ui) openLink(m *message) {
	typed := m.linkInput
	m.linkInput = ""
	if m.rendered == nil {
		return
	}
	url, ok := linkURL(m.rendered.Links, typed)
	if !ok {
		u.notice = fmt.Sprintf("No link [%s] to open", typed)
		return
	}
	if err := u.openURL(url); err != nil {
		u.notice = "Couldn't open the link: " + err.Error()
		return
	}
	u.notice = "Opened " + render.ShortLink(url, 60)
}

// xdgOpen opens a URL with the desktop's handler.
func xdgOpen(url string) error {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap it
	return nil
}
