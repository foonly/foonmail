package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/render"
	"foonmail/internal/rpc"
)

// fakeDaemon stands in for foonmaild. FetchBody stores the raw message in
// the cache, as the real daemon does.
type fakeDaemon struct {
	db  *cache.DB
	raw map[int64][]byte // by cache message id

	mu       sync.Mutex
	fetched  []int64
	flagged  []string
	synced   []int64
	sent     [][]byte
	drafts   [][]byte
	deleted  []string
	err      error // returned by every call
	flagsErr error
}

func (d *fakeDaemon) FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error) {
	id, err := d.db.Location(ctx, folderID, uid)
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	d.fetched = append(d.fetched, id)
	derr := d.err
	d.mu.Unlock()
	if derr != nil {
		return 0, derr
	}
	return id, d.db.SetRaw(ctx, id, d.raw[id], "")
}

func (d *fakeDaemon) SyncFolder(_ context.Context, folderID int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.synced = append(d.synced, folderID)
	return d.err
}

func (d *fakeDaemon) SetFlags(_ context.Context, _ int64, uids []uint32, add, remove []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flagged = append(d.flagged, fmt.Sprintf("%v +%v -%v", uids, add, remove))
	if d.flagsErr != nil {
		return d.flagsErr
	}
	return d.err
}

func (d *fakeDaemon) Acknowledge(context.Context) error { return d.err }

func (d *fakeDaemon) Send(_ context.Context, account string, raw []byte) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return 0, d.err
	}
	d.sent = append(d.sent, raw)
	return int64(len(d.sent)), nil
}

func (d *fakeDaemon) SaveDraft(_ context.Context, account string, raw []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	d.drafts = append(d.drafts, raw)
	return nil
}

func (d *fakeDaemon) DeleteMessages(_ context.Context, folderID int64, uids []uint32) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deleted = append(d.deleted, fmt.Sprintf("%d:%v", folderID, uids))
	return d.err
}

func (d *fakeDaemon) draftCalls() ([][]byte, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.drafts), slices.Clone(d.deleted)
}

func (d *fakeDaemon) sentMessages() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.sent)
}

func (d *fakeDaemon) Status(context.Context) ([]rpc.AccountStatus, error) {
	if d.err != nil {
		return nil, d.err
	}
	return []rpc.AccountStatus{{Name: "uplink", Online: true}}, nil
}

func (d *fakeDaemon) setErr(err error) {
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
}

type calls struct {
	fetched []int64
	flagged []string
	synced  []int64
}

func (d *fakeDaemon) calls() calls {
	d.mu.Lock()
	defer d.mu.Unlock()
	return calls{slices.Clone(d.fetched), slices.Clone(d.flagged), slices.Clone(d.synced)}
}

type harness struct {
	t       *testing.T
	u       *ui
	height  int // window height in px; default 700
	db      *cache.DB
	daemon  *fakeDaemon
	r       input.Router
	opened  []string
	inbox   cache.Folder
	sent    cache.Folder
	archive cache.Folder
	ids     map[string]int64 // cache message id by Message-ID
}

func rawMsg(from, subject, msgID, inReplyTo, body string) []byte {
	h := fmt.Sprintf("From: %s\r\nSubject: %s\r\nMessage-ID: <%s@test>\r\n", from, subject, msgID)
	if inReplyTo != "" {
		h += fmt.Sprintf("In-Reply-To: <%s@test>\r\nReferences: <%s@test>\r\n", inReplyTo, inReplyTo)
	}
	ct := "text/plain"
	if strings.HasPrefix(body, "<") {
		ct = "text/html"
	}
	return []byte(h + "Content-Type: " + ct + "; charset=utf-8\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n"))
}

// newHarness builds an account with a three-message thread (the middle
// reply in Sent), an unrelated read message, and an unsynced Archive.
func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := cache.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	acct, _ := db.EnsureAccount(ctx, "uplink")
	h := &harness{t: t, db: db, ids: map[string]int64{}}
	h.inbox, _ = db.EnsureFolder(ctx, acct, "INBOX")
	h.sent, _ = db.EnsureFolder(ctx, acct, "Sent")
	h.archive, _ = db.EnsureFolder(ctx, acct, "Archive")
	db.SetFolderInfo(ctx, h.inbox.ID, ".", "")
	db.SetFolderInfo(ctx, h.sent.ID, ".", `\Sent`)
	db.SetFolderInfo(ctx, h.archive.ID, ".", "")
	db.SaveSyncState(ctx, h.inbox.ID, 1, 0, 100)
	db.SaveSyncState(ctx, h.sent.ID, 1, 0, 100)

	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	type m struct {
		folder           cache.Folder
		uid              uint32
		msgID, inReplyTo string
		from, addr, subj string
		hour             int
		seen             bool
		body             string
	}
	msgs := []m{
		{h.inbox, 1, "a", "", "Bob", "bob@example.com", "Kahvi?", 9, true, `<p>See <a href="https://one.example/">one</a>.</p>`},
		{h.sent, 7, "b", "a", "Me", "niklas@uplink.fi", "Re: Kahvi?", 10, true, "Sure.\n\nOn Tue, Bob wrote:\n> Kahvi?\n"},
		{h.inbox, 2, "c", "b", "Liisa", "liisa@example.com", "Re: Re: Kahvi?", 11, false, `<p>Me too, <a href="https://two.example/">two</a>.</p>`},
		{h.inbox, 3, "x", "", "Bob", "bob@example.com", "Invoice", 8, true, "Pay it."},
	}
	d := &fakeDaemon{db: db, raw: map[int64][]byte{}}
	for _, x := range msgs {
		flags := []string{}
		if x.seen {
			flags = []string{`\Seen`}
		}
		env := cache.Envelope{MessageID: x.msgID, InReplyTo: x.inReplyTo, Subject: x.subj,
			From: cache.Address{Name: x.from, Addr: x.addr}, Date: base.Add(time.Duration(x.hour-12) * time.Hour)}
		if err := db.AddMessages(ctx, x.folder.ID, []cache.NewMessage{{UID: x.uid, Flags: flags, Envelope: env, Size: int64(x.uid)}}); err != nil {
			t.Fatal(err)
		}
		id, _ := db.Location(ctx, x.folder.ID, x.uid)
		h.ids[x.msgID] = id
		d.raw[id] = rawMsg(fmt.Sprintf("%s <%s>", x.from, x.addr), x.subj, x.msgID, x.inReplyTo, x.body)
	}
	h.inbox, _ = db.FolderByID(ctx, h.inbox.ID)
	h.sent, _ = db.FolderByID(ctx, h.sent.ID)
	h.archive, _ = db.FolderByID(ctx, h.archive.ID)

	u := newUI(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u.account = &config.Account{Name: "uplink", Email: "niklas@uplink.fi", DisplayName: "Niklas"}
	u.own = u.account.Addresses()
	u.accountID, u.folder, u.daemon = acct, h.inbox, d
	u.sent = &h.sent
	u.now = func() time.Time { return base }
	u.openURL = func(url string) error { h.opened = append(h.opened, url); return nil }
	h.u, h.daemon = u, d
	u.reload()
	h.frame()
	h.frame()
	return h
}

func (h *harness) frame() {
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Constraints: layout.Exact(image.Pt(1000, cmp.Or(h.height, 700))),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Source:      h.r.Source(),
	}
	h.u.drain()
	h.u.layout(gtx)
	h.r.Frame(&ops)
}

func (h *harness) press(name key.Name, mods ...key.Modifiers) {
	var m key.Modifiers
	for _, x := range mods {
		m |= x
	}
	h.r.Queue(key.Event{Name: name, Modifiers: m, State: key.Press})
	h.frame()
	h.frame()
}

func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.frame()
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) loaded() bool {
	if h.u.conv == nil {
		return false
	}
	for _, p := range h.u.conv.parts {
		if p.loading {
			return false
		}
	}
	return true
}

func (h *harness) selectedKey() string {
	if c := h.u.current(); c != nil {
		return c.key
	}
	return ""
}

func TestConversationList(t *testing.T) {
	h := newHarness(t)
	if len(h.u.convs) != 2 {
		t.Fatalf("%d conversations, want 2", len(h.u.convs))
	}
	c := h.u.convs[0]
	if c.key != "a" || len(c.members) != 3 || c.unread != 1 || c.people != "Liisa, me, Bob" || c.subject != "Kahvi?" {
		t.Errorf("first conversation = %+v", c)
	}
}

func TestOpenConversation(t *testing.T) {
	h := newHarness(t)
	h.height = 160 // less than one message tall, so each message can be scrolled to the top
	h.press(key.NameReturn)
	h.eventually("all parts loaded", h.loaded)
	o := h.u.conv
	if h.u.mode != modeView || len(o.parts) != 3 {
		t.Fatalf("mode %v, %d parts", h.u.mode, len(o.parts))
	}
	if o.parts[1].entry.FolderID != h.sent.ID || !strings.HasPrefix(o.parts[1].rendered.Text, "Sure.") {
		t.Errorf("the Sent reply isn't the middle message: %+v", o.parts[1].entry)
	}
	// It opens at the first unread message.
	if o.list.Position.First != 2 {
		t.Errorf("opened at message %d, want 2 (the unread one)", o.list.Position.First)
	}
	// Link numbers run on across the conversation.
	if t1, t3 := o.parts[0].rendered.Text, o.parts[2].rendered.Text; !strings.Contains(t1, "one [1]") || !strings.Contains(t3, "two [2]") {
		t.Errorf("link numbering: %q / %q", t1, t3)
	}
	// Only the unread INBOX message is marked read; Sent isn't touched.
	h.eventually("marked read", func() bool { return h.u.convs[0].unread == 0 })
	if got := h.daemon.calls().flagged; len(got) != 1 || got[0] != `[2] +[\Seen] -[]` {
		t.Errorf("SetFlags calls = %v", got)
	}

	h.press("2")
	h.press(key.NameReturn)
	if !slices.Equal(h.opened, []string{"https://two.example/"}) {
		t.Errorf("opened %v", h.opened)
	}

	// p and n move between messages.
	h.press("P")
	if o.list.Position.First != 1 {
		t.Errorf("p: at message %d, want 1", o.list.Position.First)
	}
	h.press("N")
	if o.list.Position.First != 2 {
		t.Errorf("n: at message %d, want 2", o.list.Position.First)
	}

	// j opens the next conversation; Esc returns with the keyboard working.
	h.press("J")
	h.eventually("next conversation", func() bool { return h.u.conv.conv.key == "x" && h.loaded() })
	h.press(key.NameEscape)
	h.press("K")
	if h.u.mode != modeList || h.selectedKey() != "a" {
		t.Errorf("after Esc and k: mode %v, selected %q", h.u.mode, h.selectedKey())
	}
}

func TestQuotesCollapse(t *testing.T) {
	h := newHarness(t)
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	o := h.u.conv
	if got := bodyText(o.parts[1].rendered, false); !strings.Contains(got, "quoted text from Bob, 1 lines") {
		t.Errorf("collapsed: %q", got)
	}
	h.press("H")
	if !o.showQuotes {
		t.Error("h did not show quoted text")
	}
}

func TestCachedBodiesNeedNoDaemon(t *testing.T) {
	h := newHarness(t)
	for _, k := range []string{"a", "b", "c"} {
		id := h.ids[k]
		if err := h.db.SetRaw(context.Background(), id, h.daemon.raw[id], ""); err != nil {
			t.Fatal(err)
		}
	}
	h.daemon.setErr(rpc.ErrNotRunning)
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	for i, p := range h.u.conv.parts {
		if p.err != nil {
			t.Errorf("part %d: %v", i, p.err)
		}
	}
	if got := h.daemon.calls().fetched; len(got) != 0 {
		t.Errorf("FetchBody called for cached bodies: %v", got)
	}
}

func TestDaemonDownShowsWhyAndKeepsUnread(t *testing.T) {
	h := newHarness(t)
	h.daemon.setErr(rpc.ErrNotRunning)
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	if err := h.u.conv.parts[2].err; err == nil || !strings.Contains(err.Error(), "foonmaild isn't running") {
		t.Errorf("err = %v", err)
	}
	if h.u.convs[0].unread != 1 {
		t.Error("a conversation that couldn't be shown was marked read")
	}
}

func TestToggleReadRevertsOnFailure(t *testing.T) {
	h := newHarness(t)
	h.press("N", key.ModShift)
	if h.u.convs[0].unread != 0 {
		t.Fatal("N did not mark the conversation read at once")
	}
	h.daemon.mu.Lock()
	h.daemon.flagsErr = errors.New("server said no")
	h.daemon.mu.Unlock()
	h.press("N", key.ModShift) // marks the newest unread, which fails
	h.eventually("reverted", func() bool {
		return h.u.convs[0].unread == 0 && strings.Contains(h.u.notice, "server said no")
	})
}

func TestFolderPane(t *testing.T) {
	h := newHarness(t)
	h.press("C")
	if !h.u.pane.open || !h.r.Source().Focused(h.u.pane.tag) {
		t.Fatal("c did not open the folder pane with focus")
	}
	var labels []string
	for _, r := range h.u.pane.rows {
		labels = append(labels, r.label)
	}
	if !slices.Equal(labels, []string{"INBOX", "Archive", "Sent"}) {
		t.Errorf("folders = %v", labels)
	}
	if h.u.pane.rows[h.u.pane.sel].folder.ID != h.inbox.ID {
		t.Error("the pane didn't start on the current folder")
	}

	// Moving previews the folder from the cache, without syncing it.
	h.press("J") // Archive
	h.press("J") // Sent
	if h.u.folder.ID != h.sent.ID || len(h.u.convs) != 1 {
		t.Errorf("preview: folder %q with %d conversations", h.u.folder.Name, len(h.u.convs))
	}
	if got := h.daemon.calls().synced; len(got) != 0 {
		t.Errorf("previewing synced folders: %v", got)
	}

	// Esc goes back to where we were, still without syncing.
	h.press(key.NameEscape)
	if h.u.pane.open || h.u.folder.ID != h.inbox.ID || len(h.daemon.calls().synced) != 0 {
		t.Errorf("after Esc: open %v, folder %q, synced %v", h.u.pane.open, h.u.folder.Name, h.daemon.calls().synced)
	}
	h.press("J")
	if h.selectedKey() != "x" {
		t.Errorf("the list didn't get the keyboard back: selected %q", h.selectedKey())
	}

	// ← opens it too; → chooses, which syncs and returns to the list.
	h.press(key.NameLeftArrow)
	if !h.u.pane.open {
		t.Fatal("← did not open the pane")
	}
	h.press("J") // Archive
	h.press(key.NameRightArrow)
	if h.u.pane.open || h.u.folder.ID != h.archive.ID {
		t.Fatalf("after →: open %v, folder %q", h.u.pane.open, h.u.folder.Name)
	}
	h.eventually("Archive synced once chosen", func() bool {
		return slices.Equal(h.daemon.calls().synced, []int64{h.archive.ID})
	})
	if !h.r.Source().Focused(h.u.listTag) {
		t.Error("the list should have the keyboard after choosing")
	}

	// Choosing INBOX doesn't sync it: IDLE keeps it current.
	h.press("C")
	h.press("K")
	h.press(key.NameReturn)
	if h.u.folder.ID != h.inbox.ID || len(h.daemon.calls().synced) != 1 {
		t.Errorf("back to INBOX: folder %q, synced %v", h.u.folder.Name, h.daemon.calls().synced)
	}
}

func TestPaneSlides(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.u.now = func() time.Time { return now }
	h.u.openPane()
	gtx := layout.Context{Ops: new(op.Ops), Source: h.r.Source(), Constraints: layout.Exact(image.Pt(800, 600)),
		Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	if s := h.u.paneShown(gtx); s != 0 {
		t.Errorf("at the start: %v", s)
	}
	now = now.Add(paneDuration / 2)
	if s := h.u.paneShown(gtx); s <= 0.5 || s >= 1 {
		t.Errorf("halfway (eased out): %v", s)
	}
	now = now.Add(paneDuration)
	if s := h.u.paneShown(gtx); s != 1 {
		t.Errorf("after the slide: %v", s)
	}
	h.u.closePane()
	now = now.Add(paneDuration / 2)
	if s := h.u.paneShown(gtx); s <= 0 || s >= 0.5 {
		t.Errorf("closing halfway: %v", s)
	}
	now = now.Add(paneDuration)
	if s := h.u.paneShown(gtx); s != 0 {
		t.Errorf("closed: %v", s)
	}
}

func TestSentFolderShowsItsOwnConversations(t *testing.T) {
	h := newHarness(t)
	h.u.switchFolder(h.sent)
	if len(h.u.convs) != 1 || len(h.u.convs[0].members) != 1 {
		t.Errorf("Sent: %+v", h.u.convs)
	}
}

func TestFilterKeepsSelection(t *testing.T) {
	h := newHarness(t)
	h.press("J") // "Invoice"
	h.press("/", key.ModShift)
	h.u.search.SetText("invo")
	h.u.applyFilter(h.selectedKey())
	if len(h.u.view) != 1 || h.selectedKey() != "x" {
		t.Errorf("filtered: %d shown, selected %q", len(h.u.view), h.selectedKey())
	}
	h.press(key.NameEscape)
	if h.u.searching || len(h.u.view) != 2 || h.selectedKey() != "x" {
		t.Errorf("after Esc: searching %v, %d shown, selected %q", h.u.searching, len(h.u.view), h.selectedKey())
	}
}

func TestReloadKeepsSelection(t *testing.T) {
	h := newHarness(t)
	h.press("J") // "Invoice"
	err := h.db.AddMessages(context.Background(), h.inbox.ID, []cache.NewMessage{{UID: 4, Envelope: cache.Envelope{
		MessageID: "new", Subject: "Newest", Date: time.Date(2026, 11, 4, 13, 0, 0, 0, time.UTC)}}})
	if err != nil {
		t.Fatal(err)
	}
	h.u.reload()
	if len(h.u.view) != 3 || h.selectedKey() != "x" {
		t.Errorf("after reload: %d shown, selected %q", len(h.u.view), h.selectedKey())
	}
}

func TestDisplayHelpers(t *testing.T) {
	links := []render.Link{{N: 1, URL: "https://a.example/x"}, {N: 2, URL: "file:///etc/passwd"}, {N: 3, URL: "mailto:a@b"}}
	if u, ok := linkURL(links, "1"); !ok || u != "https://a.example/x" {
		t.Errorf("link 1 = %q %v", u, ok)
	}
	if _, ok := linkURL(links, "2"); ok {
		t.Error("a file: link may not be opened")
	}
	if _, ok := linkURL(links, "3"); !ok {
		t.Error("mailto: links should open")
	}
	atts := []render.Attachment{
		{Filename: "../../evil.pdf", ContentType: "application/pdf", Size: 2048},
		{Filename: "logo.png", ContentType: "image/png", Inline: true, ContentID: "x"},
	}
	if got := attachmentLines(atts); len(got) != 1 || got[0] != "evil.pdf  (application/pdf, 2 KB)" {
		t.Errorf("attachments = %q", got)
	}
}

func TestArrowsMoveBetweenConversations(t *testing.T) {
	h := newHarness(t)
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	h.press(key.NameRightArrow)
	h.eventually("next conversation", func() bool { return h.u.conv.conv.key == "x" && h.loaded() })
	h.press(key.NameLeftArrow)
	h.eventually("previous conversation", func() bool { return h.u.conv.conv.key == "a" && h.loaded() })
	// At the first conversation, â stays put.
	h.press(key.NameLeftArrow)
	if h.u.conv.conv.key != "a" || h.u.mode != modeView {
		t.Errorf("â at the first conversation: key %q, mode %v", h.u.conv.conv.key, h.u.mode)
	}
}

func TestTextFocusIsReleasedWithoutSelection(t *testing.T) {
	h := newHarness(t)
	h.height = 160
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	o := h.u.conv
	body := &o.parts[2].body
	src := h.r.Source()

	// A selection keeps focus, so it can be copied.
	src.Execute(key.FocusCmd{Tag: body})
	body.SetCaret(0, 4)
	h.frame()
	h.frame()
	if !src.Focused(body) {
		t.Error("text lost focus while something was selected")
	}

	// While the mouse button is down (a drag), focus stays too.
	body.ClearSelection()
	h.u.pressing = true
	h.frame()
	if !src.Focused(body) {
		t.Error("text lost focus during a drag")
	}

	// A click that selects nothing gives the keyboard back.
	h.u.pressing = false
	h.frame()
	h.frame()
	if src.Focused(body) {
		t.Fatal("text kept focus with nothing selected")
	}
	h.press(key.NameRightArrow)
	h.eventually("â after clicking into the text reaches the next conversation",
		func() bool { return h.u.conv.conv.key == "x" })
}

func (h *harness) click(x, y float32, clicks int) {
	h.t.Helper()
	for range clicks {
		pos := f32.Pt(x, y)
		h.r.Queue(
			pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos},
		)
	}
	h.frame()
	h.frame()
}

func TestFolderPaneMouse(t *testing.T) {
	h := newHarness(t)
	h.u.now = time.Now // the slide needs a clock that moves
	h.press("C")
	h.eventually("pane slid out", func() bool {
		return h.u.paneShown(layout.Context{Ops: new(op.Ops), Source: h.r.Source()}) == 1
	})
	row := float32(h.u.rowH)
	y := float32(h.u.listTop) + 1.5*row // the second row: Archive

	h.click(40, y, 1)
	if h.u.pane.sel != 1 || h.u.folder.ID != h.archive.ID {
		t.Fatalf("a click didn't preview Archive: sel %d, folder %q", h.u.pane.sel, h.u.folder.Name)
	}
	if !h.u.pane.open || len(h.daemon.calls().synced) != 0 {
		t.Errorf("a single click should only preview: open %v, synced %v", h.u.pane.open, h.daemon.calls().synced)
	}

	h.click(40, y, 2)
	if h.u.pane.open || h.u.folder.ID != h.archive.ID {
		t.Errorf("a double click didn't choose Archive: open %v, folder %q", h.u.pane.open, h.u.folder.Name)
	}
	h.eventually("Archive synced", func() bool { return slices.Equal(h.daemon.calls().synced, []int64{h.archive.ID}) })

	// Back in INBOX with the pane gone, the message list takes clicks too.
	h.u.switchFolder(h.inbox)
	h.eventually("pane closed", func() bool {
		return h.u.paneShown(layout.Context{Ops: new(op.Ops), Source: h.r.Source()}) == 0
	})
	h.click(300, y, 1)
	if h.selectedKey() != "x" {
		t.Errorf("clicking the second row of the list selected %q", h.selectedKey())
	}
}
