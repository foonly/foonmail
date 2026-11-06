package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/explorer"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/render"
	"foonmail/internal/rpc"
)

// daemon is what the GUI needs from foonmaild (rpc.Client), so tests can
// use a fake. It is nil when there is no session bus.
type daemon interface {
	FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error)
	SyncFolder(ctx context.Context, folderID int64) error
	SetFlags(ctx context.Context, folderID int64, uids []uint32, add, remove []string) error
	Acknowledge(ctx context.Context) error
	Status(ctx context.Context) ([]rpc.AccountStatus, error)
	Send(ctx context.Context, account string, raw []byte) (int64, error)
	SaveDraft(ctx context.Context, account string, raw []byte) error
	DeleteMessages(ctx context.Context, folderID int64, uids []uint32) error
	MoveMessages(ctx context.Context, folderID int64, uids []uint32, dest int64) error
}

type mode int

const (
	modeList mode = iota
	modeView
	modeCompose
)

// part is one message of the open conversation.
type part struct {
	entry    cache.Entry
	loading  bool
	err      error
	rendered *render.Message
	body     widget.Selectable
}

// openConv is the conversation being read.
type openConv struct {
	conv       conversation
	parts      []*part
	showQuotes bool
	cur        int         // the message n and p move between, and replies answer
	heights    map[int]int // each message's height in px when last drawn
	linkInput  string      // digits typed: a link (Enter) or attachment (o, w) number
	list       widget.List
}

func (o *openConv) links() []render.Link {
	var out []render.Link
	for _, p := range o.parts {
		if p.rendered != nil {
			out = append(out, p.rendered.Links...)
		}
	}
	return out
}

type ui struct {
	w      *app.Window
	th     *material.Theme
	db     *cache.DB
	daemon daemon
	log    *slog.Logger
	now    func() time.Time

	account   *config.Account
	own       []string // lowercased own addresses
	accountID int64
	folder    cache.Folder
	sent      *cache.Folder // the \Sent folder, for replies shown in conversations

	entries []cache.Entry // the folder's messages, then Sent's
	convs   []conversation
	view    []int // indices into convs, after filtering
	sel     int   // index into view
	mode    mode

	list    widget.List
	listTag *int
	click   gesture.Click
	rowH    int
	viewH   int

	search    widget.Editor
	searching bool

	conv *openConv
	comp *composer

	expl       *explorer.Explorer // the portal file picker; nil in tests
	editorArgv func() []string    // the external editor command

	pane    folderPane
	listTop int // y where the folder pane and message list start

	viewPtr  *int // pointer tag over the conversation, to see the mouse button
	pressing bool // a mouse button is down in the conversation

	status        *rpc.AccountStatus // nil until known
	down          bool               // the daemon didn't answer the last Status call
	notice        string             // transient message in the status bar
	pendingDelete string             // conversation key waiting for a second d in Trash
	openURL       func(string) error // opens a link or a file; xdg-open
	downloads     func() string      // where w saves attachments
	contacts      []contact          // ranked correspondents, for address suggestions
	accounts      []*account         // every account the window can show; the shown one's fields are below
	missing       []*config.Account  // configured accounts not in the cache yet
	inboxUnread   map[int64]int      // unread mail in each account's INBOX, by account id
	dataDir       string
	undoDelay     time.Duration      // how long sent mail can be taken back
	marks         map[string]bool    // marked conversations, by key
	markBase      map[string]bool    // the marks before the current Shift range
	anchor        int                // where the current Shift range started; -1 for none
	moving        map[movingKey]bool // messages being moved or deleted, kept out of the list
	autosavePath  string             // where the message being written is kept; "" for none
	focusWindow   func()             // brings the window forward; hyprctl on Hyprland
	focusReq      event.Tag

	results chan func()

	mono                       font.Font
	muted, line, barBg, accent color.NRGBA
	selBg, selBgBlurred        color.NRGBA
	markBg                     color.NRGBA   // a marked conversation's row
	quoteColors                []color.NRGBA // by quote depth, repeating
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

// reload rereads the folder (and Sent, for replies) from the cache and
// regroups it, keeping the selected conversation.
func (u *ui) reload() {
	u.refreshInboxUnread()
	keep := ""
	if c := u.current(); c != nil {
		keep = c.key
	}
	ctx := context.Background()
	entries, err := u.db.Entries(ctx, u.folder.ID)
	if err != nil {
		u.notice = "Reading the cache failed: " + err.Error()
		return
	}
	if u.sent != nil && u.sent.ID != u.folder.ID {
		if sent, err := u.db.Entries(ctx, u.sent.ID); err == nil {
			entries = append(entries, sent...)
		}
	}
	// Messages the daemon is still moving stay out, so a reload after one
	// move of a burst doesn't bring back the others for a moment; those the
	// cache no longer has are done.
	if len(u.moving) > 0 {
		inCache := map[movingKey]bool{}
		for _, e := range entries {
			inCache[movingKey{e.FolderID, e.UID}] = true
		}
		for k := range u.moving {
			if (k.folder == u.folder.ID || (u.sent != nil && k.folder == u.sent.ID)) && !inCache[k] {
				delete(u.moving, k)
			}
		}
		entries = slices.DeleteFunc(entries, func(e cache.Entry) bool { return u.moving[movingKey{e.FolderID, e.UID}] })
	}
	u.entries = entries
	u.convs = buildConversations(entries, u.folder.ID, u.own)
	u.applyFilter(keep)
	u.pruneMarks()
}

// applyFilter rebuilds view from the search field, keeping the
// conversation with key keep selected if it is still shown.
func (u *ui) applyFilter(keep string) {
	u.view = u.view[:0]
	words := strings.Fields(u.search.Text())
	if !u.searching || len(words) == 0 {
		for i := range u.convs {
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
		for i, c := range u.convs {
			for _, m := range c.members {
				if m.FolderID == u.folder.ID && match[m.UID] {
					u.view = append(u.view, i)
					break
				}
			}
		}
	}
	u.sel = 0
	for vi, i := range u.view {
		if u.convs[i].key == keep {
			u.sel = vi
			break
		}
	}
	u.ensureVisible()
}

func (u *ui) current() *conversation {
	if u.sel < 0 || u.sel >= len(u.view) {
		return nil
	}
	return &u.convs[u.view[u.sel]]
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

// open shows the selected conversation, scrolled to its first unread
// message (or the last one), and marks its messages in the folder read.
// Bodies are fetched and rendered in the background, in order, so link
// numbers run on from one message to the next.
func (u *ui) open() {
	c := u.current()
	if c == nil {
		return
	}
	o := &openConv{conv: *c, heights: map[int]int{}}
	o.list.Axis = layout.Vertical
	target := -1
	for i, m := range c.members {
		o.parts = append(o.parts, &part{entry: m, loading: true})
		if target < 0 && m.FolderID == u.folder.ID && unread(m.Summary) {
			target = i
		}
	}
	if target < 0 {
		target = len(c.members) - 1
	}
	o.list.Position.First, o.cur = target, target
	u.conv, u.mode = o, modeView

	toMark := c.folderUIDs(u.folder.ID, true)
	go func() {
		var earlier []render.Link // the same address keeps its number across messages
		anyShown := false
		for _, p := range o.parts {
			raw, err := u.loadRaw(p.entry.Summary)
			var r *render.Message
			if err == nil {
				r, err = render.Render(raw, render.Options{Earlier: earlier, LinkPlain: true})
			}
			if r != nil {
				earlier = append(earlier, r.Links...)
				anyShown = true
			}
			u.post(func() { p.loading, p.rendered, p.err = false, r, err })
		}
		if anyShown && len(toMark) > 0 {
			u.post(func() { u.setSeen(toMark, true) })
		}
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
	switch {
	case errors.Is(err, rpc.ErrNotRunning):
		return nil, errors.New("foonmaild isn't running, and this message hasn't been downloaded yet")
	case errors.Is(err, rpc.ErrOffline):
		return nil, errors.New("offline, and this message hasn't been downloaded yet")
	case err != nil:
		return nil, err
	}
	raw, err := u.db.Raw(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && raw == nil) {
		return nil, errors.New("the message disappeared from the cache")
	}
	return raw, err
}

// setSeen marks messages of the current folder read or unread: at once in
// the list, then on the server through the daemon, undoing the change if
// that fails. It runs on the UI goroutine.
func (u *ui) setSeen(uids []uint32, seen bool) {
	if len(uids) == 0 {
		return
	}
	if u.daemon == nil {
		u.notice = "foonmaild isn't running: can't change flags"
		return
	}
	folder := u.folder.ID
	apply := func(on bool) {
		if u.folder.ID != folder {
			return
		}
		set := map[uint32]bool{}
		for _, uid := range uids {
			set[uid] = true
		}
		for i := range u.entries {
			if e := &u.entries[i]; e.FolderID == folder && set[e.UID] {
				e.Flags = setFlag(e.Flags, `\Seen`, on)
			}
		}
		keep := ""
		if c := u.current(); c != nil {
			keep = c.key
		}
		u.convs = buildConversations(u.entries, folder, u.own)
		u.applyFilter(keep)
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
		if err := u.daemon.SetFlags(ctx, folder, uids, add, remove); err != nil {
			u.post(func() { apply(!seen); u.notice = "Couldn't change the flag: " + err.Error() })
		}
	}()
}

// toggleRead marks the targeted conversations read if any of them is unread,
// otherwise marks each one's newest message in the folder unread.
func (u *ui) toggleRead() {
	convs := u.targets()
	if len(convs) == 0 {
		return
	}
	// Any unread: all read. All read: each one's latest message unread.
	var unread, latest []uint32
	for _, c := range convs {
		unread = append(unread, c.folderUIDs(u.folder.ID, true)...)
		if all := c.folderUIDs(u.folder.ID, false); len(all) > 0 {
			latest = append(latest, all[len(all)-1])
		}
	}
	if len(unread) > 0 {
		u.setSeen(unread, true)
	} else if len(latest) > 0 {
		u.setSeen(latest, false)
	}
}

// switchFolder shows another folder and syncs it.
func (u *ui) switchFolder(f cache.Folder) {
	u.showFolder(f)
	u.mode = modeList
	u.focusReq = u.listTag
	u.syncFolder(f)
}

// showFolder shows a folder's cached contents.
func (u *ui) showFolder(f cache.Folder) {
	if f.AccountID != u.accountID {
		if a := u.accountByID(f.AccountID); a != nil {
			u.useAccount(a)
		}
	}
	u.folder = f
	u.clearMarks()
	u.search.SetText("")
	u.searching = false
	u.list.Position = layout.Position{}
	u.sel = 0
	u.reload()
}

// syncFolder syncs a folder in the background; the cached copy shows
// meanwhile. INBOX is kept current by IDLE and needs no sync.
func (u *ui) syncFolder(f cache.Folder) {
	if f.Name == "INBOX" || u.daemon == nil {
		return
	}
	syncing := "Syncing " + f.Name + "…"
	u.notice = syncing
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := u.daemon.SyncFolder(ctx, f.ID)
		u.post(func() {
			switch {
			case err == nil:
				if u.notice == syncing {
					u.notice = ""
				}
			case errors.Is(err, rpc.ErrOffline) || errors.Is(err, rpc.ErrNotRunning):
				u.notice = f.Name + ": offline, showing the cached copy"
			default:
				u.notice = "Syncing " + f.Name + " failed: " + err.Error()
			}
		})
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
func (u *ui) openLink(o *openConv) {
	typed := o.linkInput
	o.linkInput = ""
	url, ok := linkURL(o.links(), typed)
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
