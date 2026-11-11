package main

import (
	"testing"

	"gioui.org/io/key"
)

func TestHelp(t *testing.T) {
	h := newHarness(t)
	h.press("?", key.ModShift)
	if h.u.mode != modeHelp || h.u.help.list.Position.First != 1 {
		t.Fatalf("? in the list: mode %v, at section %d", h.u.mode, h.u.help.list.Position.First)
	}
	h.press("J")
	h.press("?", key.ModShift)
	if h.u.mode != modeList {
		t.Fatalf("? again: mode %v", h.u.mode)
	}
	// The list keeps the keys after the help closes.
	h.press("J")
	if h.selectedKey() != "x" {
		t.Errorf("j after the help: selected %q", h.selectedKey())
	}

	// From a conversation it opens at reading, and Esc goes back there.
	h.press(key.NameReturn)
	h.press("?", key.ModShift)
	if got := helpSections[h.u.help.list.Position.First].title; got != "Reading a conversation" {
		t.Errorf("from a conversation, opens at %q", got)
	}
	h.press(key.NameEscape)
	if h.u.mode != modeView {
		t.Errorf("Esc: mode %v", h.u.mode)
	}

	// F1 works while writing, where ? is typed.
	h.press("M")
	h.press(key.NameF1)
	if h.u.mode != modeHelp || helpSections[h.u.help.list.Position.First].title != "Writing" {
		t.Fatalf("F1 while writing: mode %v", h.u.mode)
	}
	h.press("Q")
	if h.u.mode != modeCompose {
		t.Errorf("q: mode %v", h.u.mode)
	}
}
