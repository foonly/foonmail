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
