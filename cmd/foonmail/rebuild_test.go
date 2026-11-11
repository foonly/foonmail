package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"foonmail/internal/cache"
	"foonmail/internal/render"
)

// TestRebuiltFromQuotes checks that a reply whose earlier messages never
// reached this account shows them, read back from its quote, before it.
func TestRebuiltFromQuotes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	date := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	env := cache.Envelope{MessageID: "late", InReplyTo: "never-here", Subject: "Re: Plans",
		From: cache.Address{Name: "Bob", Addr: "bob@example.com"}, Date: date}
	if err := h.db.AddMessages(ctx, h.inbox.ID, []cache.NewMessage{{UID: 20, Flags: []string{}, Envelope: env, Size: 1}}); err != nil {
		t.Fatal(err)
	}
	id, _ := h.db.Location(ctx, h.inbox.ID, 20)
	h.daemon.raw[id] = rawMsg("Bob <bob@example.com>", "Re: Plans", "late", "never-here",
		"Adding Kim.\n\nOn Wed, 4 Nov 2026 at 13:30, Liisa <liisa@example.com> wrote:\n\n"+
			"> Friday works.\n>\n"+
			"> On Wed, 4 Nov 2026 at 13:00, Bob <bob@example.com> wrote:\n>\n"+
			">> Lunch on Friday?\n")
	h.u.reload()
	h.frame()
	h.u.sel = -1
	for vi, ci := range h.u.view {
		for _, m := range h.u.convs[ci].members {
			if m.UID == 20 {
				h.u.sel = vi
			}
		}
	}
	if h.u.sel < 0 {
		t.Fatal("the new conversation isn't in the list")
	}
	h.press(key.NameReturn)
	h.eventually("rebuilt", func() bool { return h.u.conv != nil && len(h.u.conv.parts) == 3 })
	o := h.u.conv
	var got []string
	for _, p := range o.parts {
		if p.rebuilt != nil {
			got = append(got, p.rebuilt.Who+": "+p.rebuilt.Text)
		} else {
			got = append(got, "real")
		}
	}
	if strings.Join(got, "|") != "Bob: Lunch on Friday?|Liisa: Friday works.|real" {
		t.Errorf("parts = %q", got)
	}
	if o.cur != 2 || o.rebuilt != 2 {
		t.Errorf("current %d, rebuilt %d; the real message should stay current", o.cur, o.rebuilt)
	}

	// On a rebuilt message, keys act on the real one that quoted it.
	h.press("P")
	if o.cur != 1 {
		t.Fatalf("after p, current %d", o.cur)
	}
	h.press("R", key.ModShift)
	h.eventually("compose", func() bool { return h.u.mode == modeCompose })
	if c := h.u.comp; c.to.Text() != "Bob <bob@example.com>" || c.base.InReplyTo != "late@test" {
		t.Errorf("reply goes to %q, answers %q", c.to.Text(), c.base.InReplyTo)
	}
}

// TestRebuildMissing checks when messages are rebuilt and where the chain
// stops: at a quoted message the conversation already has.
func TestRebuildMissing(t *testing.T) {
	m, err := render.Render([]byte("From: Bob <bob@example.com>\r\nContent-Type: text/plain\r\n\r\n"+
		"Third.\r\n\r\nOn Wed, 4 Nov 2026 at 13:30, Liisa <liisa@example.com> wrote:\r\n\r\n> Second.\r\n>\r\n"+
		"> On Wed, 4 Nov 2026 at 13:00, Kim <kim@example.org> wrote:\r\n>\r\n>> First.\r\n"), render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	e := cache.Entry{InReplyTo: "second", MessageID: "third",
		Summary: cache.Summary{ID: 3, From: cache.Address{Addr: "liisa@example.com"}, Date: time.Date(2026, 11, 4, 13, 31, 0, 0, time.Local)}}
	// The reply itself is known, from Liisa a minute after the quote of
	// her earlier message, and doesn't stop the chain.
	if got := rebuildMissing(e, m, []cache.Entry{e}); len(got) != 2 {
		t.Errorf("reply known: %d rebuilt", len(got))
	}
	if got := rebuildMissing(e, m, nil); len(got) != 2 {
		t.Errorf("nothing known: %d rebuilt", len(got))
	}
	// The parent is here: nothing is missing.
	if got := rebuildMissing(e, m, []cache.Entry{{MessageID: "second"}}); len(got) != 0 {
		t.Errorf("parent known: %d rebuilt", len(got))
	}
	// Kim's first message is here (its time in UTC, the quote's local).
	first := time.Date(2026, 11, 4, 13, 0, 40, 0, time.Local)
	known := []cache.Entry{{MessageID: "first", Summary: cache.Summary{From: cache.Address{Addr: "KIM@example.org"}, Date: first.UTC()}}}
	if got := rebuildMissing(e, m, known); len(got) != 1 || got[0].Who != "Liisa" {
		t.Errorf("first known: %+v", got)
	}
	// Without In-Reply-To the message starts a conversation: its quote may
	// be a forward, not earlier messages of this one.
	if got := rebuildMissing(cache.Entry{}, m, nil); len(got) != 0 {
		t.Errorf("no In-Reply-To: %d rebuilt", len(got))
	}
}
