package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/widget"

	"foonmail/internal/cache"
)

// typeText types into the focused field as the keyboard would.
func (h *harness) typeText(s string) {
	for _, r := range s {
		at := h.caretRange()
		h.r.Queue(key.EditEvent{Range: at, Text: string(r)})
		h.r.Queue(key.SelectionEvent{Start: at.Start + 1, End: at.Start + 1})
		h.frame()
	}
	h.frame()
}

// caretRange is the selection of the focused compose field, where typed
// text goes.
func (h *harness) caretRange() key.Range {
	c := h.u.comp
	for _, f := range []*widget.Editor{&c.from, &c.to, &c.cc, &c.bcc, &c.subject, &c.body} {
		if h.r.Source().Focused(f) {
			s, e := f.Selection()
			return key.Range{Start: s, End: e}
		}
	}
	return key.Range{}
}

func TestRankContacts(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ranked := rankContacts([]cache.Correspondent{
		{Name: "Old Friend", Addr: "old@example.com", Sent: 10, Last: now.AddDate(-3, 0, 0)},
		{Name: "Kim Ström", Addr: "kim@example.net", Sent: 2, Last: now.AddDate(0, 0, -3)},
		{Name: "Newsletter", Addr: "news@example.com", Received: 9, Last: now},
		{Addr: "no-reply@shop.example", Received: 40, Last: now},
		{Addr: "noreply@bank.example", Sent: 1, Last: now}, // you wrote to it, so it stays
	}, now)
	var order []string
	for _, c := range ranked {
		order = append(order, c.addr)
	}
	if want := "kim@example.net news@example.com noreply@bank.example old@example.com"; strings.Join(order, " ") != want {
		t.Errorf("order %v", order)
	}

	cases := map[string][]string{
		"strom":       {"kim@example.net"}, // accents don't matter
		"kim st":      {"kim@example.net"}, // words in order
		"st kim":      nil,                 // ...not out of order
		"KIM@":        {"kim@example.net"}, // the address, any case
		"n":           {"news@example.com", "noreply@bank.example"},
		"friend":      {"old@example.com"},
		"example.com": nil, // the middle of an address isn't a start
	}
	for typed, want := range cases {
		var got []string
		for _, c := range suggestions(ranked, typed, nil, 5) {
			got = append(got, c.addr)
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%q: %v, want %v", typed, got, want)
		}
	}
	if got := suggestions(ranked, "kim", []string{"KIM@example.net"}, 5); len(got) != 0 {
		t.Errorf("an address already in the field was suggested: %v", got)
	}
}

func TestCurrentEntry(t *testing.T) {
	text := "Bob <bob@example.com>, Ström"
	if e, s := currentEntry(text, len([]rune(text))); e != " Ström" || s != 22 {
		t.Errorf("entry %q at %d", e, s)
	}
	if e, s := currentEntry("li", 2); e != "li" || s != 0 {
		t.Errorf("entry %q at %d", e, s)
	}
}

func TestContactString(t *testing.T) {
	cases := []struct {
		c    contact
		want string
	}{
		{contact{name: "Bob", addr: "bob@example.com"}, "Bob <bob@example.com>"},
		{contact{addr: "bob@example.com"}, "bob@example.com"},
		{contact{name: "Ström, Kim", addr: "kim@example.net"}, `"Ström, Kim" <kim@example.net>`},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("%q, want %q", got, tc.want)
		}
	}
}

func TestAddressSuggestions(t *testing.T) {
	h := newHarness(t)
	h.press("M")
	h.eventually("contacts loaded", func() bool { return len(h.u.contacts) > 0 })
	c := h.u.comp

	h.typeText("li")
	if len(c.sugg) != 1 || c.sugg[0].addr != "liisa@example.com" || c.suggField != &c.to {
		t.Fatalf("suggestions for li: %+v", c.sugg)
	}
	h.press(key.NameTab)
	if got := c.to.Text(); got != "Liisa <liisa@example.com>, " {
		t.Fatalf("To after Tab: %q", got)
	}
	if !h.r.Source().Focused(&c.to) || len(c.sugg) != 0 {
		t.Errorf("Tab should fill in and stay in To; suggestions %v", c.sugg)
	}

	// A second address; ↓ picks among several, Enter fills in.
	h.typeText("b")
	if len(c.sugg) != 1 || c.sugg[0].addr != "bob@example.com" {
		t.Fatalf("suggestions for b: %+v", c.sugg)
	}
	h.press(key.NameDownArrow) // wraps around a list of one
	h.press(key.NameReturn)
	if got := c.to.Text(); got != "Liisa <liisa@example.com>, Bob <bob@example.com>, " {
		t.Fatalf("To: %q", got)
	}

	// Esc closes the list without starting to discard the message.
	h.typeText("li")
	if len(c.sugg) != 0 {
		t.Errorf("Liisa is already in To but suggested again: %+v", c.sugg)
	}
	c.to.SetText("")
	h.typeText("b")
	h.press(key.NameEscape)
	if h.u.mode != modeCompose || c.discarding || len(c.sugg) != 0 {
		t.Errorf("Esc: mode %v, discarding %v, suggestions %v", h.u.mode, c.discarding, c.sugg)
	}
	h.typeText("o")
	if len(c.sugg) != 1 {
		t.Errorf("typing again didn't bring the list back")
	}

	// Sending works with the ", " a filled-in address leaves.
	c.to.SetText("Bob <bob@example.com>, ")
	c.subject.SetText("Hei")
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("sent", func() bool { return len(h.daemon.sentMessages()) == 1 })
	if m := h.lastSent(); len(m.To) != 1 || m.To[0].Addr != "bob@example.com" {
		t.Errorf("sent to %+v", m.To)
	}
}

func TestAutosaveAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unsent-home.json")
	h := newHarness(t)
	h.u.autosavePath = path
	h.press("M")
	c := h.u.comp
	h.typeText("bob@example.com")
	h.press(key.NameTab) // nothing suggested for a full address: Tab moves on
	h.u.focusReq = &c.body
	h.frame()
	h.typeText("Hei")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("saved while still typing")
	}

	base := h.u.now()
	h.u.now = func() time.Time { return base.Add(autosaveDelay + time.Second) }
	h.frame()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("not saved after the pause:", err)
	}
	var s unsent
	if err := json.Unmarshal(data, &s); err != nil || s.Draft.Body != "Hei" || s.Draft.To != "bob@example.com" {
		t.Fatalf("saved %s (%v)", data, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("autosave file mode %v", info.Mode().Perm())
	}

	// foonmail starts again: the message comes back, and Esc asks first.
	h2 := newHarness(t)
	h2.u.autosavePath = path
	h2.u.restoreUnsent()
	h2.frame()
	if h2.u.mode != modeCompose || h2.u.comp.body.Text() != "Hei" || !strings.HasPrefix(h2.u.notice, "Restored") {
		t.Fatalf("restore: mode %v, body %q, notice %q", h2.u.mode, h2.u.comp.body.Text(), h2.u.notice)
	}
	h2.press(key.NameEscape)
	if h2.u.mode != modeCompose {
		t.Error("one Esc threw away a restored message")
	}

	// Sending it removes the copy.
	h2.press(key.NameReturn, key.ModCtrl)
	h2.eventually("sent", func() bool { return h2.u.mode == modeList })
	if _, err := os.Stat(path); err == nil {
		t.Error("the autosaved copy outlived sending")
	}
}
