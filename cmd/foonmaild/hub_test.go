package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/imapsync"
	"foonmail/internal/notify"
	"foonmail/internal/tray"
)

type fakeNotifier struct {
	caps []string
	mu   sync.Mutex
	sent []*notify.Notification
	next uint32
}

func (f *fakeNotifier) Server() notify.Server { return notify.Server{Capabilities: f.caps} }

func (f *fakeNotifier) Send(n *notify.Notification) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	if n.ReplacesID != 0 {
		return n.ReplacesID, nil
	}
	f.next++
	return f.next, nil
}

func (f *fakeNotifier) all() []*notify.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*notify.Notification(nil), f.sent...)
}

type fakeTray struct {
	mu   sync.Mutex
	last tray.State
}

func (f *fakeTray) Set(s tray.State) {
	f.mu.Lock()
	f.last = s
	f.mu.Unlock()
}

func (f *fakeTray) state() tray.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

type hubEnv struct {
	t     *testing.T
	h     *hub
	db    *cache.DB
	inbox int64
	notes *fakeNotifier
	tray  *fakeTray
}

func newHubEnv(t *testing.T, details bool, accounts ...string) *hubEnv {
	t.Helper()
	if len(accounts) == 0 {
		accounts = []string{"home"}
	}
	db, err := cache.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{}
	for _, name := range accounts {
		d := details
		cfg.Accounts = append(cfg.Accounts, config.Account{
			Name: name, Email: "me@example.org", Aliases: []string{"postmaster@example.org"}, NotifyDetails: &d,
		})
	}
	e := &hubEnv{t: t, db: db, notes: &fakeNotifier{caps: []string{"actions"}}, tray: &fakeTray{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.h, err = newHub(ctx, cfg, db, e.notes, e.tray, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e.h.delay = 20 * time.Millisecond
	e.inbox = e.h.accounts[accounts[0]].inbox
	go e.h.run(ctx)
	return e
}

func (e *hubEnv) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s; tray %+v, %d notifications", what, e.tray.state(), len(e.notes.all()))
}

// arrive stores a message in the cache's INBOX, as sync does, and reports it.
func (e *hubEnv) arrive(uid uint32, from, subject string, seen bool) {
	e.t.Helper()
	var flags []string
	if seen {
		flags = []string{`\Seen`}
	}
	env := cache.Envelope{Subject: subject, From: cache.Address{Name: strings.Split(from, "@")[0], Addr: from}}
	if err := e.db.AddMessages(context.Background(), e.inbox, []cache.NewMessage{{UID: uid, Flags: flags, Envelope: env}}); err != nil {
		e.t.Fatal(err)
	}
	e.h.notify(imapsync.NewMail{Account: "home", UID: uid, Envelope: env, Seen: seen})
}

func TestSingleMailNotifiesAndLightsTray(t *testing.T) {
	e := newHubEnv(t, true)
	e.h.notify(imapsync.ConnectionChanged{Account: "home", Online: true})
	e.h.notify(imapsync.UnreadChanged{Account: "home", Unread: 404})
	e.eventually("tray online", func() bool { return strings.Contains(e.tray.state().Tooltip, "home: 404 unread") })
	if s := e.tray.state(); s.Status != tray.Active || s.IconName != "mail-read" {
		t.Errorf("idle tray = %+v; 404 old unread messages are not new mail", s)
	}

	e.arrive(1, "bob@example.com", "Kahvitauko?", false)
	e.eventually("notification", func() bool { return len(e.notes.all()) == 1 })
	n := e.notes.all()[0]
	if n.Summary != "bob" || n.Body != "Kahvitauko?" || n.Category != "email.arrived" || n.Icon != "mail-unread" {
		t.Errorf("notification = %+v", n)
	}
	if len(n.Actions) != 1 || n.Actions[0].Key != "default" {
		t.Errorf("actions = %+v", n.Actions)
	}
	e.eventually("tray shows new mail", func() bool {
		s := e.tray.state()
		return s.Status == tray.NeedsAttention && s.IconName == "mail-unread" && strings.Contains(s.Tooltip, "1 new message")
	})

	// Clicking the notification means the user is looking.
	n.OnAction("default")
	e.eventually("tray back to normal after the click", func() bool { return e.tray.state().Status == tray.Active })
}

func TestBurstBecomesSummaryUpdatedInPlace(t *testing.T) {
	e := newHubEnv(t, true)
	for i := uint32(1); i <= 5; i++ {
		e.arrive(i, "sender@example.com", "Subject "+string(rune('A'+i-1)), false)
	}
	e.eventually("summary", func() bool { return len(e.notes.all()) == 1 })
	s := e.notes.all()[0]
	if s.Summary != "5 new messages" {
		t.Errorf("summary = %q", s.Summary)
	}
	if lines := strings.Split(s.Body, "\n"); len(lines) != 4 || lines[3] != "and 2 more" || lines[0] != "sender: Subject A" {
		t.Errorf("body = %q", s.Body)
	}

	// More mail while the summary is still open updates it.
	e.arrive(6, "other@example.com", "Late", false)
	e.eventually("updated summary", func() bool { return len(e.notes.all()) == 2 })
	u := e.notes.all()[1]
	if u.ReplacesID != 1 || u.Summary != "6 new messages" {
		t.Errorf("update = replaces %d, %q", u.ReplacesID, u.Summary)
	}

	// Once it's closed, the next mail gets its own notification again.
	u.OnClosed(1, notify.ClosedDismissed)
	time.Sleep(20 * time.Millisecond)
	e.arrive(7, "bob@example.com", "Fresh", false)
	e.eventually("fresh notification", func() bool { return len(e.notes.all()) == 3 })
	if f := e.notes.all()[2]; f.ReplacesID != 0 || f.Summary != "bob" {
		t.Errorf("after close = replaces %d, %q", f.ReplacesID, f.Summary)
	}
}

func TestSkippedMailIsNotNew(t *testing.T) {
	e := newHubEnv(t, true)
	e.arrive(1, "bob@example.com", "Read on phone", true)
	e.arrive(2, "Postmaster@Example.ORG", "From me", false)
	time.Sleep(60 * time.Millisecond)
	if n := e.notes.all(); len(n) != 0 {
		t.Errorf("notified %d times; read mail and own mail must not notify", len(n))
	}
	if e.tray.state().Status != tray.Active {
		t.Error("skipped mail lit the tray")
	}
}

func TestReadElsewhereStopsBeingNew(t *testing.T) {
	e := newHubEnv(t, true)
	e.arrive(1, "bob@example.com", "One", false)
	e.arrive(2, "bob@example.com", "Two", false)
	e.eventually("two new", func() bool { return strings.Contains(e.tray.state().Tooltip, "2 new messages") })

	// Read on the phone: sync updates the cache, then reports the unread count.
	if err := e.db.SetFlags(context.Background(), e.inbox, []cache.FlagUpdate{{UID: 1, Flags: []string{`\Seen`}}}); err != nil {
		t.Fatal(err)
	}
	e.h.notify(imapsync.UnreadChanged{Account: "home", Unread: 1})
	e.eventually("one new", func() bool { return strings.Contains(e.tray.state().Tooltip, "1 new message") })

	// Deleted on the phone.
	if err := e.db.RemoveUIDs(context.Background(), e.inbox, []uint32{2}); err != nil {
		t.Fatal(err)
	}
	e.h.notify(imapsync.UnreadChanged{Account: "home", Unread: 0})
	e.eventually("nothing new", func() bool { return e.tray.state().Status == tray.Active })
}

func TestTrayClickClearsNew(t *testing.T) {
	e := newHubEnv(t, true)
	e.arrive(1, "bob@example.com", "One", false)
	e.eventually("new", func() bool { return e.tray.state().Status == tray.NeedsAttention })
	e.h.activate()
	e.eventually("cleared", func() bool { return e.tray.state().Status == tray.Active })
}

func TestNoDetails(t *testing.T) {
	e := newHubEnv(t, false)
	e.arrive(1, "bob@example.com", "Secret plans", false)
	e.eventually("notification", func() bool { return len(e.notes.all()) == 1 })
	n := e.notes.all()[0]
	if n.Summary != "New mail" || n.Body != "" {
		t.Errorf("notify_details=false showed %q / %q", n.Summary, n.Body)
	}
}

func TestOfflineInTooltip(t *testing.T) {
	e := newHubEnv(t, true)
	e.eventually("connecting", func() bool { return strings.Contains(e.tray.state().Tooltip, "home: connecting…") })
	e.h.notify(imapsync.ConnectionChanged{Account: "home", Online: false, Err: io.ErrUnexpectedEOF})
	e.eventually("offline", func() bool {
		return strings.Contains(e.tray.state().Tooltip, "home: offline (unexpected EOF)")
	})
}

func TestNoActionsWithoutServerSupport(t *testing.T) {
	e := newHubEnv(t, true)
	e.notes.caps = nil
	e.arrive(1, "bob@example.com", "Hi", false)
	e.eventually("notification", func() bool { return len(e.notes.all()) == 1 })
	if a := e.notes.all()[0].Actions; len(a) != 0 {
		t.Errorf("actions sent to a server without support: %+v", a)
	}
}
