package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"foonmail/internal/cache"
)

// addBulk puts n single-message conversations at the top of INBOX,
// "Bulk n" (UID 100+n) first, and returns their UIDs newest first.
func (h *harness) addBulk(n int) []uint32 {
	h.t.Helper()
	var msgs []cache.NewMessage
	var uids []uint32
	for i := n; i >= 1; i-- {
		uid := uint32(100 + i)
		msgs = append(msgs, cache.NewMessage{UID: uid, Flags: []string{`\Seen`}, Envelope: cache.Envelope{
			MessageID: fmt.Sprintf("bulk%d", i), Subject: fmt.Sprintf("Bulk %d", i),
			From: cache.Address{Name: "Shop", Addr: "shop@example.com"},
			Date: time.Date(2026, 11, 5, 8, i, 0, 0, time.UTC)}})
		uids = append(uids, uid)
	}
	if err := h.db.AddMessages(context.Background(), h.inbox.ID, msgs); err != nil {
		h.t.Fatal(err)
	}
	h.u.reload()
	h.u.sel = 0
	h.frame()
	return uids
}

func (h *harness) subjects() string {
	var out []string
	for _, i := range h.u.view {
		out = append(out, h.u.convs[i].subject)
	}
	return strings.Join(out, ", ")
}

// Pressing d quickly: each press takes the selected conversation, and
// reloads while the daemon still works through the moves don't bring the
// others back.
func TestRapidMovesKeepTheList(t *testing.T) {
	h := newHarness(t)
	h.addTrash()
	uids := h.addBulk(4)
	for range 3 {
		h.press("D")
	}
	want := "Bulk 1, Kahvi?, Invoice"
	if got := h.subjects(); got != want {
		t.Fatalf("after three d:\n got %s\nwant %s", got, want)
	}
	if h.u.current().subject != "Bulk 1" {
		t.Errorf("selected %q", h.u.current().subject)
	}
	// The daemon has moved the first one; the cache still has the others.
	h.db.RemoveUIDs(context.Background(), h.inbox.ID, uids[:1])
	h.u.reload()
	if got := h.subjects(); got != want || h.u.current().subject != "Bulk 1" {
		t.Errorf("after a reload mid-burst: %s, selected %q", got, h.u.current().subject)
	}
	if len(h.u.moving) != 2 {
		t.Errorf("%d messages still in flight, want 2", len(h.u.moving))
	}
	// All moved: nothing is held back any more.
	h.db.RemoveUIDs(context.Background(), h.inbox.ID, uids[1:3])
	h.u.reload()
	if len(h.u.moving) != 0 || h.subjects() != want {
		t.Errorf("after the burst: %d in flight, %s", len(h.u.moving), h.subjects())
	}
}

func TestMarkAndArchive(t *testing.T) {
	h := newHarness(t)
	uids := h.addBulk(5) // Bulk 5 … Bulk 1, Kahvi?, Invoice
	// Shift+j twice marks three; Shift+k takes one back.
	h.press("J", key.ModShift)
	h.press("J", key.ModShift)
	h.press("J", key.ModShift)
	h.press("K", key.ModShift)
	if h.u.markCount() != 3 || h.u.sel != 2 {
		t.Fatalf("after the range: %d marked, at %d", h.u.markCount(), h.u.sel)
	}
	// Space adds a separate one and moves on; a plain move keeps the marks.
	h.press("J")
	h.press("J")
	h.press(key.NameSpace)
	if h.u.markCount() != 4 || h.u.sel != 5 {
		t.Fatalf("after Space: %d marked, at %d", h.u.markCount(), h.u.sel)
	}
	if !strings.Contains(h.u.convs[h.u.view[4]].subject, "Bulk 1") {
		t.Fatalf("row 4 is %q", h.u.convs[h.u.view[4]].subject)
	}

	h.press("A")
	want := fmt.Sprintf("%d:[%d %d %d %d]->%d", h.inbox.ID, uids[0], uids[1], uids[2], uids[4], h.archive.ID)
	h.eventually("moved", func() bool { return slices.Contains(h.daemon.moveCalls(), want) })
	if got := h.subjects(); got != "Bulk 2, Kahvi?, Invoice" || len(h.u.marks) != 0 {
		t.Errorf("after archiving the marked: %s, %d marks", got, len(h.u.marks))
	}
	if h.u.current().subject != "Kahvi?" {
		t.Errorf("selected %q", h.u.current().subject)
	}

	// Esc clears marks first, without leaving anything.
	h.press(key.NameSpace)
	h.press(key.NameEscape)
	if len(h.u.marks) != 0 || h.u.mode != modeList {
		t.Errorf("Esc: %d marks, mode %v", len(h.u.marks), h.u.mode)
	}
}

func TestMoveToFolder(t *testing.T) {
	h := newHarness(t)
	h.addBulk(2) // Bulk 2, Bulk 1, Kahvi?, Invoice
	h.press(key.NameSpace)
	h.press(key.NameSpace)
	h.press("V")
	if !h.u.pane.open || len(h.u.pane.moving) != 2 {
		t.Fatalf("v: pane open %v, moving %v", h.u.pane.open, h.u.pane.moving)
	}
	// Moving the cursor doesn't preview; INBOX itself is refused.
	h.press(key.NameReturn)
	if !strings.Contains(h.u.notice, "Already in") || !h.u.pane.open {
		t.Errorf("Enter on INBOX: %q", h.u.notice)
	}
	h.press("J")
	h.press("J")
	if h.u.folder.ID != h.inbox.ID {
		t.Error("moving the cursor previewed a folder")
	}
	h.press(key.NameReturn)
	want := fmt.Sprintf("%d:[102 101]->%d", h.inbox.ID, h.archive.ID)
	h.eventually("moved", func() bool { return slices.Contains(h.daemon.moveCalls(), want) })
	if h.u.pane.open || h.subjects() != "Kahvi?, Invoice" {
		t.Errorf("after the move: pane open %v, %s", h.u.pane.open, h.subjects())
	}

	// From the conversation view, v moves the open conversation; Esc
	// cancels without moving anything.
	h.press(key.NameReturn)
	h.eventually("open", h.loaded)
	h.press("V")
	if h.u.mode != modeList || !h.u.pane.open || len(h.u.pane.moving) != 1 || h.u.pane.moving[0] != "a" {
		t.Fatalf("v in the view: mode %v, pane %v, moving %v", h.u.mode, h.u.pane.open, h.u.pane.moving)
	}
	h.press(key.NameEscape)
	if h.u.pane.open || len(h.daemon.moveCalls()) != 1 {
		t.Errorf("Esc: pane %v, moves %v", h.u.pane.open, h.daemon.moveCalls())
	}
}
