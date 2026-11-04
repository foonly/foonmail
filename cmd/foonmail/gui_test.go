package main

import (
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

	"gioui.org/io/input"
	"gioui.org/io/key"
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
	raw map[uint32][]byte

	mu       sync.Mutex
	fetched  []uint32
	flagged  []string
	err      error // returned by every call
	flagsErr error
}

func (d *fakeDaemon) FetchBody(ctx context.Context, folderID int64, uid uint32) (int64, error) {
	d.mu.Lock()
	d.fetched = append(d.fetched, uid)
	err := d.err
	d.mu.Unlock()
	if err != nil {
		return 0, err
	}
	id, err := d.db.Location(ctx, folderID, uid)
	if err != nil {
		return 0, err
	}
	return id, d.db.SetRaw(ctx, id, d.raw[uid], "")
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

func (d *fakeDaemon) Status(context.Context) ([]rpc.AccountStatus, error) {
	if d.err != nil {
		return nil, d.err
	}
	return []rpc.AccountStatus{{Name: "uplink", Online: true}}, nil
}

func (d *fakeDaemon) calls() ([]uint32, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.fetched), slices.Clone(d.flagged)
}

type harness struct {
	t      *testing.T
	u      *ui
	db     *cache.DB
	daemon *fakeDaemon
	r      input.Router
	opened []string
}

const (
	plainMsg = "From: Bob <bob@example.com>\r\nSubject: Kahvi\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Käydäänkö kahvilla?\r\n\r\nOn Tue, Liisa <liisa@example.com> wrote:\r\n> Earlier\r\n> message\r\n"
	htmlMsg = "From: News <news@example.com>\r\nSubject: Letter\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<p>Read <a href="https://example.com/post">this</a> or <a href="javascript:x()">that</a>.</p>`
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := cache.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	acct, _ := db.EnsureAccount(ctx, "uplink")
	inbox, _ := db.EnsureFolder(ctx, acct, "INBOX")
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	var msgs []cache.NewMessage
	for i, subj := range []string{"Kahvi", "Letter", "Third"} {
		flags := []string{}
		if i == 2 {
			flags = []string{`\Seen`}
		}
		msgs = append(msgs, cache.NewMessage{UID: uint32(i + 1), Flags: flags, Envelope: cache.Envelope{
			Subject: subj, From: cache.Address{Name: "Bob", Addr: "bob@example.com"},
			Date: base.Add(-time.Duration(i) * time.Hour), // UID 1 is newest
		}})
	}
	if err := db.AddMessages(ctx, inbox.ID, msgs); err != nil {
		t.Fatal(err)
	}
	inbox, _ = db.EnsureFolder(ctx, acct, "INBOX")

	d := &fakeDaemon{db: db, raw: map[uint32][]byte{1: []byte(plainMsg), 2: []byte(htmlMsg), 3: []byte(plainMsg)}}
	u := newUI(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	u.account = &config.Account{Name: "uplink"}
	u.accountID, u.folder, u.daemon = acct, inbox, d
	u.now = func() time.Time { return base }
	h := &harness{t: t, u: u, db: db, daemon: d}
	u.openURL = func(url string) error { h.opened = append(h.opened, url); return nil }
	u.reload()
	h.frame()
	h.frame()
	return h
}

func (h *harness) frame() {
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Constraints: layout.Exact(image.Pt(1000, 700)),
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

func (h *harness) selectedUID() uint32 {
	if s := h.u.current(); s != nil {
		return s.UID
	}
	return 0
}

func TestListOpenAndBack(t *testing.T) {
	h := newHarness(t)
	if len(h.u.view) != 3 || h.selectedUID() != 1 {
		t.Fatalf("list: %d items, selected uid %d; want newest first", len(h.u.view), h.selectedUID())
	}

	h.press(key.NameReturn)
	h.eventually("message rendered", func() bool { return h.u.msg != nil && h.u.msg.rendered != nil })
	if h.u.mode != modeView || !strings.HasPrefix(h.u.msg.rendered.Text, "Käydäänkö kahvilla?") {
		t.Errorf("mode %v, text %q", h.u.mode, h.u.msg.rendered.Text)
	}
	h.eventually("marked read", func() bool { return !unread(h.u.items[0]) })
	fetched, flags := h.daemon.calls()
	if !slices.Equal(fetched, []uint32{1}) || len(flags) != 1 || flags[0] != `[1] +[\Seen] -[]` {
		t.Errorf("daemon calls: fetched %v, flags %v", fetched, flags)
	}

	// j in the message view opens the next message.
	h.press("J")
	h.eventually("next message", func() bool { return h.u.msg.sum.UID == 2 && h.u.msg.rendered != nil })

	// Esc goes back, and the list has the keyboard again.
	h.press(key.NameEscape)
	if h.u.mode != modeList {
		t.Fatal("Esc did not return to the list")
	}
	h.press("J")
	if h.selectedUID() != 3 {
		t.Errorf("j after returning: selected uid %d, want 3", h.selectedUID())
	}
}

func TestCachedBodyNeedsNoDaemon(t *testing.T) {
	h := newHarness(t)
	h.daemon.raw[1] = []byte(plainMsg)
	id, _ := h.db.Location(context.Background(), h.u.folder.ID, 1)
	if err := h.db.SetRaw(context.Background(), id, []byte(plainMsg), ""); err != nil {
		t.Fatal(err)
	}
	h.daemon.err = rpc.ErrNotRunning
	h.press(key.NameReturn)
	h.eventually("message rendered from the cache", func() bool { return h.u.msg.rendered != nil })
	if fetched, _ := h.daemon.calls(); len(fetched) != 0 {
		t.Errorf("FetchBody called for a cached body: %v", fetched)
	}
}

func TestDaemonDownShowsWhy(t *testing.T) {
	h := newHarness(t)
	h.daemon.err = rpc.ErrNotRunning
	h.press(key.NameReturn)
	h.eventually("error shown", func() bool { return h.u.msg.err != nil })
	if !strings.Contains(h.u.msg.err.Error(), "foonmaild isn't running") {
		t.Errorf("err = %v", h.u.msg.err)
	}
	if !unread(h.u.items[0]) {
		t.Error("a message that couldn't be shown was marked read")
	}
}

func TestToggleReadRevertsOnFailure(t *testing.T) {
	h := newHarness(t)
	h.press("N", key.ModShift)
	if unread(h.u.items[0]) {
		t.Fatal("N did not mark the message read at once")
	}
	h.daemon.mu.Lock()
	h.daemon.flagsErr = errors.New("server said no")
	h.daemon.mu.Unlock()
	h.press("N", key.ModShift) // back to unread, which fails
	h.eventually("reverted", func() bool { return !unread(h.u.items[0]) && strings.Contains(h.u.notice, "server said no") })
}

func TestQuotesAndLinks(t *testing.T) {
	h := newHarness(t)
	h.press(key.NameReturn)
	h.eventually("rendered", func() bool { return h.u.msg.rendered != nil })
	m := h.u.msg
	if got := bodyText(m.rendered, false); !strings.Contains(got, "[… quoted text from Liisa, 2 lines · h shows it]") || strings.Contains(got, "Earlier") {
		t.Errorf("collapsed: %q", got)
	}
	h.press("H")
	if !m.showQuotes || !strings.Contains(bodyText(m.rendered, m.showQuotes), "> Earlier") {
		t.Error("h did not show the quoted text")
	}

	h.press("J") // the HTML message with links
	h.eventually("html rendered", func() bool { return h.u.msg.sum.UID == 2 && h.u.msg.rendered != nil })
	h.press("1")
	h.press(key.NameReturn)
	if !slices.Equal(h.opened, []string{"https://example.com/post"}) {
		t.Errorf("opened %v", h.opened)
	}
	// The javascript: link has no number to open; a missing number is reported.
	h.press("7")
	h.press(key.NameReturn)
	if len(h.opened) != 1 || !strings.Contains(h.u.notice, "No link [7]") {
		t.Errorf("opened %v, notice %q", h.opened, h.u.notice)
	}
	// Esc with a number typed only clears it.
	h.press("1")
	h.press(key.NameEscape)
	if h.u.mode != modeView || h.u.msg.linkInput != "" {
		t.Error("Esc with a link number typed should clear it, not leave the message")
	}
}

func TestFilterKeepsSelection(t *testing.T) {
	h := newHarness(t)
	h.press("J") // select uid 2, "Letter"
	h.press("/", key.ModShift)
	if !h.u.searching {
		t.Fatal("/ did not open the filter")
	}
	h.u.search.SetText("lett")
	h.u.applyFilter(h.selectedUID())
	if len(h.u.view) != 1 || h.selectedUID() != 2 {
		t.Errorf("filtered: %d shown, selected %d", len(h.u.view), h.selectedUID())
	}
	h.press(key.NameEscape)
	if h.u.searching || len(h.u.view) != 3 || h.selectedUID() != 2 {
		t.Errorf("after Esc: searching %v, %d shown, selected %d", h.u.searching, len(h.u.view), h.selectedUID())
	}
}

func TestReloadKeepsSelection(t *testing.T) {
	h := newHarness(t)
	h.press("J")
	h.press("J") // uid 3
	// A newer message arrives (the daemon wrote it; FolderChanged reloads).
	err := h.db.AddMessages(context.Background(), h.u.folder.ID, []cache.NewMessage{{UID: 4, Envelope: cache.Envelope{
		Subject: "Newest", Date: time.Date(2026, 11, 4, 13, 0, 0, 0, time.UTC)}}})
	if err != nil {
		t.Fatal(err)
	}
	h.u.reload()
	if len(h.u.view) != 4 || h.selectedUID() != 3 {
		t.Errorf("after reload: %d shown, selected %d, want 4 and uid 3", len(h.u.view), h.selectedUID())
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
	if got := setFlag([]string{`\Seen`, `\Flagged`}, `\Seen`, false); !slices.Equal(got, []string{`\Flagged`}) {
		t.Errorf("setFlag = %v", got)
	}
}
