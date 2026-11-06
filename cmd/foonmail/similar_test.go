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

func TestAlike(t *testing.T) {
	for _, c := range []struct {
		nameA, addrA, subjA, nameB, addrB, subjB string
		want                                     bool
	}{
		{"Driving Glasses #1", "a@spam1.example", "Silmiesi ei pitäisi kärsiä ratin takana.",
			"Driving Glasses", "b@spam2.example", "Silmiesi ei pitäisi kärsiä ratin takana!", true},
		{"Shop", "shop@example.com", "Your order 123 has shipped", "Shop", "shop@example.com", "Your order 456 has shipped", true},
		{"Shop", "shop@example.com", "Your order has shipped", "Shop", "shop@example.com", "Weekly newsletter", false},
		{"Bob", "bob@example.com", "Invoice", "Alice", "alice@other.example", "Invoice", false},
		{"Club One", "news@club.example", "Sale now", "Club One Offers", "deals@club.example", "Sale now on", true},
	} {
		if got := alike(c.nameA, c.addrA, c.subjA, c.nameB, c.addrB, c.subjB); got != c.want {
			t.Errorf("alike(%q %q, %q %q) = %v", c.nameA, c.subjA, c.nameB, c.subjB, got)
		}
	}
}

// s on spam shows the rest of the run, marked; s again junks those that
// stay marked, and the list comes back.
func TestJunkFindsLookAlikes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	junk, _ := h.db.EnsureFolder(ctx, h.u.accountID, "Junk")
	h.db.SetFolderInfo(ctx, junk.ID, ".", `\Junk`)
	var msgs []cache.NewMessage
	for i, name := range []string{"Driving Glasses", "Driving Glasses #1", "Driving Glasses #2", "Driving Glasses"} {
		subject := "Silmiesi ei pitäisi kärsiä ratin takana."
		if i == 3 {
			subject = "Your receipt" // same name, other subject: not the same spam
		}
		msgs = append(msgs, cache.NewMessage{UID: uint32(200 + i), Envelope: cache.Envelope{
			MessageID: fmt.Sprintf("spam%d", i), Subject: subject,
			From: cache.Address{Name: name, Addr: fmt.Sprintf("x%d@spam%d.example", i, i)},
			Date: time.Date(2026, 11, 5, 9, 10-i, 0, 0, time.UTC)}})
	}
	h.db.AddMessages(ctx, h.inbox.ID, msgs)
	h.u.reload()
	h.u.sel = 0
	h.frame()

	h.press("S")
	if h.u.similar == nil || h.u.markCount() != 2 || len(h.u.view) != 2 {
		t.Fatalf("after s: similar %v, %d marked, %d shown: %s", h.u.similar, h.u.markCount(), len(h.u.view), h.subjects())
	}
	if !strings.Contains(h.u.notice, "2 more look similar") {
		t.Errorf("notice %q", h.u.notice)
	}
	// Keep one: unmark it, junk the other.
	h.press(key.NameSpace)
	h.press("S")
	want := fmt.Sprintf("%d:[202]->%d", h.inbox.ID, junk.ID)
	h.eventually("moved", func() bool { return slices.Contains(h.daemon.moveCalls(), want) })
	if h.u.similar == nil || len(h.u.view) != 1 {
		t.Fatalf("after junking the marked: similar %v, %s", h.u.similar, h.subjects())
	}
	h.press(key.NameEscape)
	if h.u.similar != nil || len(h.u.view) != 4 { // Driving Glasses #1, receipt, Kahvi?, Invoice
		t.Errorf("after Esc: %s", h.subjects())
	}
}
