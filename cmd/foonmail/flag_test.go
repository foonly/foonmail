package main

import (
	"slices"
	"testing"

	"gioui.org/io/key"
)

func (h *harness) flagCalls() []string {
	h.daemon.mu.Lock()
	defer h.daemon.mu.Unlock()
	return slices.Clone(h.daemon.flagged)
}

func TestToggleFlagged(t *testing.T) {
	h := newHarness(t) // Kahvi? (INBOX UIDs 1 and 2), Invoice (3)
	h.press("!", key.ModShift)
	if c := h.u.current(); !c.flagged {
		t.Fatal("! didn't flag the conversation")
	}
	h.press("!", key.ModShift)
	if c := h.u.current(); c.flagged {
		t.Fatal("! again didn't unflag it")
	}
	// The newest message is flagged; unflagging clears it again.
	want := []string{`[2] +[\Flagged] -[]`, `[2] +[] -[\Flagged]`}
	if got := h.flagCalls(); !slices.Equal(got, want) {
		t.Errorf("daemon calls %q, want %q", got, want)
	}

	// Marked conversations go together, and from the conversation view too.
	h.press(key.NameSpace)
	h.press(key.NameSpace)
	h.press("!", key.ModShift)
	h.press(key.NameReturn)
	h.press("!", key.ModShift)
	want = append(want, `[2 3] +[\Flagged] -[]`)
	if got := h.flagCalls(); len(got) != 4 || got[2] != want[2] {
		t.Errorf("daemon calls %q", got)
	}
}
