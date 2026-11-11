package main

import (
	"strings"
	"testing"

	"gioui.org/io/key"

	"foonmail/internal/sieve"
)

func (h *harness) filterState() (string, int) {
	h.daemon.mu.Lock()
	defer h.daemon.mu.Unlock()
	return h.daemon.filterScript, h.daemon.filterPuts
}

func TestFilterFromMessage(t *testing.T) {
	h := newHarness(t) // INBOX: Kahvi? (Bob, then Liisa), Invoice (Bob); Archive empty
	h.press("J")       // Invoice, from bob@example.com
	h.press("F", key.ModShift)
	if h.u.mode != modeFilters {
		t.Fatalf("Shift+F: mode %v", h.u.mode)
	}
	h.eventually("the filters loaded", func() bool { return !h.u.filters.loading })
	if !strings.Contains(h.u.filterHints(), "like the selected message") {
		t.Errorf("hints %q", h.u.filterHints())
	}

	// n starts a rule for the sender; ←/→ on the match switch to the
	// domain, with the value following until it is typed over.
	h.press("N")
	f := h.u.filters.form
	if f == nil || f.value.Text() != "bob@example.com" {
		t.Fatalf("form %+v", f)
	}
	h.u.focusReq = f.match
	h.frame()
	h.press(key.NameRightArrow)
	if sieve.Fields[f.field] != sieve.FromDomain || f.value.Text() != "example.com" {
		t.Errorf("→: %s %q", sieve.Fields[f.field], f.value.Text())
	}
	h.press(key.NameLeftArrow)

	// The folder is chosen from suggestions: "arch" is completed to
	// Archive by the first Enter, the second saves.
	h.u.focusReq = &f.folder
	h.frame()
	f.folder.SetText("arch")
	h.frame()
	h.press(key.NameReturn)
	if f.folder.Text() != "Archive" {
		t.Fatalf("Enter on a partial name: %q", f.folder.Text())
	}
	h.press(key.NameReturn)
	h.eventually("saved", func() bool { _, n := h.filterState(); return n == 1 && h.u.filters.form == nil })
	script, _ := h.filterState()
	if rules, byHand := sieve.Parse(script); byHand || len(rules) != 1 ||
		rules[0] != (sieve.Rule{Field: sieve.FromAddress, Value: "bob@example.com", Folder: "Archive"}) {
		t.Fatalf("stored rules %v (by hand %v)", rules, byHand)
	}

	// Both conversations Bob wrote in are offered for moving; y moves them.
	if !strings.Contains(h.u.notice, "y moves the 2 conversations in INBOX that match to Archive") {
		t.Fatalf("notice %q", h.u.notice)
	}
	h.press("Y")
	if h.u.mode != modeList {
		t.Errorf("after y: mode %v", h.u.mode)
	}
	h.eventually("the move", func() bool { return len(h.daemon.moveCalls()) > 0 })
}

func TestFilterDeleteAndRejected(t *testing.T) {
	h := newHarness(t)
	h.daemon.filterScript = sieve.Script([]sieve.Rule{
		{Field: sieve.Subject, Value: "Invoice", Folder: "Archive"},
		{Field: sieve.FromDomain, Value: "example.com", Folder: "Archive", MarkRead: true},
	})
	h.press("F", key.ModShift)
	h.eventually("the filters loaded", func() bool { return !h.u.filters.loading })
	if len(h.u.filters.rules) != 2 {
		t.Fatalf("rules %v", h.u.filters.rules)
	}
	// Shift+j moves the first rule down; d asks, the second d deletes.
	h.press("J", key.ModShift)
	h.eventually("moved", func() bool {
		r := h.u.filters.rules
		return len(r) == 2 && r[1].Field == sieve.Subject && h.u.filters.sel == 1
	})
	h.press("D")
	if !strings.Contains(h.u.notice, "d again deletes") {
		t.Fatalf("first d: %q", h.u.notice)
	}
	h.press("D")
	h.eventually("deleted", func() bool { return len(h.u.filters.rules) == 1 })

	// A rule the server rejects keeps the form open and says why.
	h.press("N")
	f := h.u.filters.form
	f.value.SetText("broken")
	f.folder.SetText("Archive")
	h.u.focusReq = &f.folder
	h.frame()
	h.press(key.NameReturn)
	h.eventually("the rejection", func() bool { return strings.Contains(h.u.notice, "weren't saved") })
	if h.u.filters.form == nil || !strings.Contains(h.u.notice, "unknown command") {
		t.Errorf("after a rejection: form %v, notice %q", h.u.filters.form != nil, h.u.notice)
	}
	h.press(key.NameEscape)
	h.press(key.NameEscape)
	if h.u.mode != modeList {
		t.Errorf("Esc twice: mode %v", h.u.mode)
	}
}

func TestFilterScriptByHand(t *testing.T) {
	h := newHarness(t)
	h.daemon.filterScript = "require \"fileinto\";\nif true { fileinto \"X\"; }\n"
	h.press("F", key.ModShift)
	h.eventually("the filters loaded", func() bool { return !h.u.filters.loading })
	if !h.u.filters.byHand || !strings.HasPrefix(h.u.filterHints(), "e edit") {
		t.Errorf("by hand %v, hints %q", h.u.filters.byHand, h.u.filterHints())
	}
	h.press("N")
	if h.u.filters.form != nil {
		t.Error("n made a rule over a hand-written script")
	}
}

// The window moves focus on an unhandled Tab (app.Window calls MoveFocus);
// the form's parts must be in that order.
func TestFilterFormTab(t *testing.T) {
	h := newHarness(t)
	h.press("F", key.ModShift)
	h.eventually("the filters loaded", func() bool { return !h.u.filters.loading })
	h.press("N")
	f := h.u.filters.form
	src := h.r.Source()
	where := func() string {
		switch {
		case src.Focused(f.match):
			return "match"
		case src.Focused(&f.value):
			return "value"
		case src.Focused(&f.folder):
			return "folder"
		case src.Focused(f.read):
			return "read"
		}
		return "?"
	}
	var got []string
	for range 5 {
		got = append(got, where())
		h.r.MoveFocus(key.FocusForward)
		h.frame()
	}
	if strings.Join(got, " ") != "folder read match value folder" {
		t.Errorf("Tab goes %v", got)
	}
	// Space on the read line ticks it.
	h.u.focusReq = f.read
	h.frame()
	h.press(key.NameSpace)
	if !f.markRd {
		t.Error("Space didn't tick mark-read")
	}
}
