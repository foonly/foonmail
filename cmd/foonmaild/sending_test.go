package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"foonmail/internal/outbox"
	"foonmail/internal/rpc"
)

type fakeOutbox struct {
	mu       sync.Mutex
	undoErr  error
	undone   []int64
	resolved []string
	sendErr  error
	sent     [][]byte
}

func (f *fakeOutbox) Undo(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undone = append(f.undone, id)
	return f.undoErr
}

func (f *fakeOutbox) Resolve(_ context.Context, id int64, resend bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if resend {
		f.resolved = append(f.resolved, "resend")
	} else {
		f.resolved = append(f.resolved, "sent")
	}
	return nil
}

func (f *fakeOutbox) Submit(_ context.Context, account string, raw []byte) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.sent = append(f.sent, raw)
	return int64(len(f.sent)), nil
}

func (f *fakeOutbox) List(context.Context) ([]outbox.Item, error) {
	return []outbox.Item{{ID: 1, Account: "home", State: outbox.Uncertain, Subject: "Kahvi?", To: "Bob",
		Error: "no answer", Created: time.Unix(1700000000, 0)}}, nil
}

func (f *fakeOutbox) snapshot() ([]int64, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.undone), slices.Clone(f.resolved)
}

func TestSendingNotifications(t *testing.T) {
	e := newHubEnv(t, true)
	fo := &fakeOutbox{undoErr: outbox.ErrTooLate}
	e.h.outbox = fo

	e.h.notifyOutbox(outbox.Event{Kind: outbox.Queued, ID: 7, Account: "home", Subject: "Kahvi?", To: "Bob"})
	e.eventually("sending notification", func() bool { return len(e.notes.all()) == 1 })
	q := e.notes.all()[0]
	if q.Summary != "Sending to Bob" || q.Body != "Kahvi?" || len(q.Actions) != 1 || q.Actions[0].Key != "undo" {
		t.Errorf("queued notification = %+v", q)
	}

	// Undo pressed too late: the same notification says so.
	q.OnAction("undo")
	e.eventually("too-late notice", func() bool { return len(e.notes.all()) == 2 })
	late := e.notes.all()[1]
	if late.Summary != "Too late to undo" || late.ReplacesID != 1 {
		t.Errorf("too-late notification = %+v", late)
	}
	if undone, _ := fo.snapshot(); !slices.Equal(undone, []int64{7}) {
		t.Errorf("Undo called with %v", undone)
	}

	// Sent replaces it too, and is short-lived.
	e.h.notifyOutbox(outbox.Event{Kind: outbox.SentOK, ID: 7, Account: "home", Subject: "Kahvi?", To: "Bob"})
	e.eventually("sent notification", func() bool { return len(e.notes.all()) == 3 })
	s := e.notes.all()[2]
	if s.Summary != "Sent to Bob" || s.ReplacesID != 1 || s.Timeout <= 0 || len(s.Actions) != 0 {
		t.Errorf("sent notification = %+v", s)
	}
}

func TestUncertainAndFailedNotifications(t *testing.T) {
	e := newHubEnv(t, true)
	fo := &fakeOutbox{}
	e.h.outbox = fo

	e.h.notifyOutbox(outbox.Event{Kind: outbox.UncertainSend, ID: 3, Subject: "Report", To: "Liisa",
		Err: errors.New("connection reset")})
	e.eventually("uncertain notification", func() bool { return len(e.notes.all()) == 1 })
	u := e.notes.all()[0]
	if !strings.Contains(u.Summary, "may or may not have been sent") || u.Urgency != 2 || u.Timeout >= 0 {
		t.Errorf("uncertain notification = %+v", u)
	}
	keys := []string{}
	for _, a := range u.Actions {
		keys = append(keys, a.Key)
	}
	if !slices.Equal(keys, []string{"resend", "sent"}) {
		t.Fatalf("actions = %v", keys)
	}
	u.OnAction("sent")
	e.eventually("resolved", func() bool { _, r := fo.snapshot(); return slices.Equal(r, []string{"sent"}) })

	e.h.notifyOutbox(outbox.Event{Kind: outbox.FailedSend, ID: 4, Subject: "Big", Err: errors.New("554 too big")})
	e.eventually("failed notification", func() bool { return len(e.notes.all()) == 2 })
	if f := e.notes.all()[1]; !strings.Contains(f.Summary, "Couldn't send") || f.Body != "554 too big" || f.Timeout >= 0 {
		t.Errorf("failed notification = %+v", f)
	}
}

func TestNoUndoButtonWithoutActions(t *testing.T) {
	e := newHubEnv(t, true)
	e.notes.caps = nil
	e.h.notifyOutbox(outbox.Event{Kind: outbox.Queued, ID: 1, Subject: "x", To: "Bob"})
	e.eventually("notification", func() bool { return len(e.notes.all()) == 1 })
	if a := e.notes.all()[0].Actions; len(a) != 0 {
		t.Errorf("actions without server support: %+v", a)
	}
}

func TestServiceSending(t *testing.T) {
	e := newServiceEnv(t)
	fo := e.outbox
	ctx := callCtx(t)

	id, err := e.client.Send(ctx, "home", []byte("To: bob@example.com\r\n\r\nHi\r\n"))
	if err != nil || id != 1 {
		t.Errorf("Send = %d, %v", id, err)
	}
	items, err := e.client.Outbox(ctx)
	if err != nil || len(items) != 1 || items[0] != (rpc.OutboxItem{ID: 1, Account: "home", State: "uncertain",
		Subject: "Kahvi?", To: "Bob", Error: "no answer", Created: 1700000000}) {
		t.Errorf("Outbox = %+v, %v", items, err)
	}
	if err := e.client.Resolve(ctx, 1, true); err != nil {
		t.Errorf("Resolve: %v", err)
	}

	fo.mu.Lock()
	fo.undoErr = outbox.ErrTooLate
	fo.sendErr = errors.Join(outbox.ErrTooLarge, errors.New("9 MB, the limit is 5 MB"))
	fo.mu.Unlock()
	if err := e.client.Undo(ctx, 1); !errors.Is(err, rpc.ErrTooLate) {
		t.Errorf("Undo too late: err = %v", err)
	}
	if _, err := e.client.Send(ctx, "home", []byte("x")); !errors.Is(err, rpc.ErrTooLarge) || !strings.Contains(err.Error(), "limit is 5 MB") {
		t.Errorf("Send too large: err = %v", err)
	}
}

type fakeGUI struct {
	mu    sync.Mutex
	calls []string
}

func (g *fakeGUI) Show(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, "show")
	return nil
}

func (g *fakeGUI) ShowMessage(_ context.Context, folder int64, uid uint32) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, fmt.Sprintf("message %d %d", folder, uid))
	return nil
}

func (g *fakeGUI) all() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.calls)
}

func TestClicksOpenTheGUI(t *testing.T) {
	e := newHubEnv(t, true)
	gui := &fakeGUI{}
	e.h.gui = gui
	e.arrive(9, "bob@example.com", "Hi", false)
	e.eventually("notification", func() bool { return len(e.notes.all()) == 1 })
	e.notes.all()[0].OnAction("default")
	want := fmt.Sprintf("message %d 9", e.inbox)
	e.eventually("GUI asked to show the message", func() bool { return slices.Contains(gui.all(), want) })

	e.h.activate() // the tray
	e.eventually("GUI raised from the tray", func() bool { return slices.Contains(gui.all(), "show") })
}
