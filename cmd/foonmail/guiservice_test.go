package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/key"

	"foonmail/internal/dbustest"
	"foonmail/internal/rpc"
)

func TestGUIServiceShowsMessagesAndIsSingleInstance(t *testing.T) {
	h := newHarness(t)
	bus := dbustest.Start(t)
	var raised atomic.Int32
	h.u.focusWindow = func() { raised.Add(1) }
	if err := exportGUI(bus.Conn(), h.u); err != nil {
		t.Fatal(err)
	}
	// A second foonmail on the same session finds the name taken.
	if err := exportGUI(bus.Conn(), h.u); !errors.Is(err, errAlreadyRunning) {
		t.Errorf("second instance: err = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gui := rpc.NewGUIClient(bus.Conn())
	h.press("J") // look at something else first
	if err := gui.ShowMessage(ctx, h.inbox.ID, 2); err != nil {
		t.Fatal(err)
	}
	h.eventually("conversation opened", func() bool {
		return h.u.mode == modeView && h.u.conv != nil && h.u.conv.conv.key == "a"
	})
	if raised.Load() == 0 {
		t.Error("the window wasn't brought forward")
	}

	// Opening a message in another folder switches to it.
	if err := gui.ShowMessage(ctx, h.sent.ID, 7); err != nil {
		t.Fatal(err)
	}
	h.eventually("Sent conversation opened", func() bool {
		if h.u.folder.ID != h.sent.ID || h.u.mode != modeView {
			return false
		}
		for _, m := range h.u.conv.conv.members {
			if m.FolderID == h.sent.ID && m.UID == 7 {
				return true
			}
		}
		return false
	})

	// While writing, a click doesn't throw the message away.
	h.press(key.NameEscape)
	h.press("M")
	if err := gui.ShowMessage(ctx, h.inbox.ID, 2); err != nil {
		t.Fatal(err)
	}
	h.eventually("still composing", func() bool { return h.u.mode == modeCompose && h.u.notice != "" })

	if err := gui.Show(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestShowMessageOpensAtThatMessage checks that a notification opens the
// conversation at its message, not at the first unread one, with the
// keys working, and that the messages new when it opened stay shown so.
func TestShowMessageOpensAtThatMessage(t *testing.T) {
	h := newHarness(t)
	h.u.searching = true // the filter had focus
	h.u.focusReq = &h.u.search
	h.frame()

	h.u.showMessage(h.inbox.ID, 1) // Bob's first message, already read
	h.frame()
	o := h.u.conv
	if h.u.mode != modeView || o == nil || o.conv.key != "a" {
		t.Fatalf("mode %v, conv %v", h.u.mode, o)
	}
	if e := o.parts[o.cur].entry; e.FolderID != h.inbox.ID || e.UID != 1 {
		t.Errorf("current message is %d/%d, want the notified one", e.FolderID, e.UID)
	}
	if o.list.Position.First != o.cur {
		t.Errorf("scrolled to %d, current is %d", o.list.Position.First, o.cur)
	}
	var news []uint32
	for _, p := range o.parts {
		if p.unread {
			news = append(news, p.entry.UID)
		}
	}
	if len(news) != 1 || news[0] != 2 {
		t.Errorf("new messages %v, want [2]", news)
	}
	h.eventually("loaded", h.loaded)
	h.eventually("marked read", func() bool {
		for _, e := range h.u.entries {
			if e.UID == 2 && e.FolderID == h.inbox.ID {
				return !unread(e.Summary)
			}
		}
		return false
	})
	if !o.parts[2].unread {
		t.Error("the message stopped being shown as new once read")
	}

	// The keys go to the conversation: n moves to the next message.
	h.press("N")
	if o.cur != 1 {
		t.Errorf("after n, current is %d", o.cur)
	}

	// Without a message to go to, the first unread one is current.
	h.press(key.NameEscape)
	h.u.open()
	if c := h.u.conv; c.parts[c.cur].entry.UID != 2 {
		t.Errorf("plain open: current UID %d", c.parts[c.cur].entry.UID)
	}
}
