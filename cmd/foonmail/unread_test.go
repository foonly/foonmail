package main

import (
	"context"
	"testing"

	"gioui.org/io/key"

	"foonmail/internal/cache"
)

func TestUnreadOnly(t *testing.T) {
	h := newHarness(t) // Kahvi? (one unread), Invoice (read)
	h.press("U")
	if got := h.subjects(); got != "Kahvi?" {
		t.Fatalf("unread only: %s", got)
	}
	// Reading it doesn't take it out of the list under you.
	h.db.SetFlags(context.Background(), h.inbox.ID, []cache.FlagUpdate{{UID: 2, Flags: []string{`\Seen`}}})
	h.u.reload()
	if got := h.subjects(); got != "Kahvi?" {
		t.Errorf("after reading it: %s", got)
	}
	h.press(key.NameEscape)
	if h.u.onlyUnread || h.subjects() != "Kahvi?, Invoice" {
		t.Errorf("Esc: unread only %v, %s", h.u.onlyUnread, h.subjects())
	}
	// Turned on again, the read one is gone.
	h.press("U")
	if len(h.u.view) != 0 {
		t.Errorf("nothing unread, still shown: %s", h.subjects())
	}
}
