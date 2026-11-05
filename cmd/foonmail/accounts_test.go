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
	"foonmail/internal/config"
)

// addSecondAccount puts a "work" account with one INBOX message in the
// harness's cache, and gives the window both accounts.
func addSecondAccount(t *testing.T, h *harness) *account {
	t.Helper()
	ctx := context.Background()
	id, _ := h.db.EnsureAccount(ctx, "work")
	inbox, _ := h.db.EnsureFolder(ctx, id, "INBOX")
	h.db.SetFolderInfo(ctx, inbox.ID, ".", "")
	h.db.SaveSyncState(ctx, inbox.ID, 1, 0, 100)
	env := cache.Envelope{MessageID: "w1", Subject: "Quarterly report", From: cache.Address{Name: "Pia", Addr: "pia@work.example"},
		Date: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)}
	if err := h.db.AddMessages(ctx, inbox.ID, []cache.NewMessage{{UID: 1, Envelope: env}}); err != nil {
		t.Fatal(err)
	}
	msgID, _ := h.db.Location(ctx, inbox.ID, 1)
	h.ids["w1"] = msgID
	h.daemon.raw[msgID] = rawMsg("Pia <pia@work.example>", "Quarterly report", "w1", "", "Numbers attached.")

	home, err := loadAccount(ctx, h.db, h.u.account, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, err := loadAccount(ctx, h.db, &config.Account{Name: "work", Email: "kim@work.example", DisplayName: "Kim Ström"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.u.accounts = []*account{home, work}
	return work
}

func paneLabels(h *harness) []string {
	var out []string
	for _, r := range h.u.pane.rows {
		l := r.label
		switch {
		case r.header:
			l = "#" + l
		case r.other:
			l = fmt.Sprintf("+%s(%d)", l, r.counts.Unread)
		}
		out = append(out, l)
	}
	return out
}

func TestSeveralAccounts(t *testing.T) {
	h := newHarness(t)
	work := addSecondAccount(t, h)
	home := h.u.accounts[0]

	// The shown account's folders, and the other account on one line with
	// its unread mail.
	h.press("C")
	if got, want := strings.Join(paneLabels(h), " "), "#home INBOX Sent Archive +work(1)"; got != want {
		t.Fatalf("pane rows:\n got %s\nwant %s", got, want)
	}
	if r := h.u.pane.rows[h.u.pane.sel]; r.header || r.folder.ID != home.inbox.ID {
		t.Errorf("selected %+v, want home's INBOX", r)
	}

	// Moving onto the other account switches to it and unfolds its folders.
	h.press("J")
	h.press("J")
	h.press("J")
	if got, want := strings.Join(paneLabels(h), " "), "+home(1) #work INBOX"; got != want {
		t.Fatalf("after moving onto work:\n got %s\nwant %s", got, want)
	}
	if r := h.u.pane.rows[h.u.pane.sel]; r.folder.ID != work.inbox.ID || h.u.accountID != work.id {
		t.Fatalf("selected %+v, account %d", r, h.u.accountID)
	}
	if h.u.current() == nil || h.u.current().subject != "Quarterly report" {
		t.Errorf("work's INBOX isn't shown")
	}
	// Up passes over work's name to home, which unfolds again.
	h.press("K")
	if got := strings.Join(paneLabels(h), " "); got != "#home INBOX Sent Archive +work(1)" || h.u.accountID != home.id {
		t.Errorf("k from work's INBOX: %s, account %d", got, h.u.accountID)
	}
	if r := h.u.pane.rows[h.u.pane.sel]; r.folder.ID != home.inbox.ID {
		t.Errorf("selected %+v", r)
	}
	// Esc goes back to where the pane opened, account and all.
	h.press(key.NameEnd)
	h.press(key.NameEscape)
	if h.u.accountID != home.id || h.u.folder.ID != home.inbox.ID {
		t.Errorf("Esc: account %d folder %d", h.u.accountID, h.u.folder.ID)
	}

	// Choose work's INBOX; a new message is from work and sent through it.
	h.press("C")
	h.press(key.NameEnd)
	h.press(key.NameReturn)
	if h.u.accountID != work.id || h.u.pane.open {
		t.Fatalf("Enter: account %d, pane open %v", h.u.accountID, h.u.pane.open)
	}
	h.press("M")
	c := h.u.comp
	if got := c.from.Text(); got != "Kim Ström <kim@work.example>" {
		t.Errorf("From %q", got)
	}
	c.to.SetText("pia@work.example")
	c.subject.SetText("Re: numbers")
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("sent", func() bool { return len(h.daemon.sentMessages()) == 1 })

	// Changing From to another account's address sends through that one.
	h.press("M")
	h.u.comp.from.SetText("Kim <me@example.org>")
	h.u.comp.to.SetText("bob@example.com")
	h.press(key.NameReturn, key.ModCtrl)
	h.eventually("sent", func() bool { return len(h.daemon.sentMessages()) == 2 })
	h.daemon.mu.Lock()
	via := slices.Clone(h.daemon.sentVia)
	h.daemon.mu.Unlock()
	if fmt.Sprint(via) != "[work home]" {
		t.Errorf("sent via %v", via)
	}
}

func TestTabSwitchesAccount(t *testing.T) {
	h := newHarness(t)
	work := addSecondAccount(t, h)
	home := h.u.accounts[0]
	h.u.reload()
	if h.u.inboxUnread[work.id] != 1 || h.u.inboxUnread[home.id] != 1 {
		t.Errorf("unread per account: %v", h.u.inboxUnread)
	}
	h.press(key.NameTab)
	if h.u.accountID != work.id || h.u.folder.ID != work.inbox.ID {
		t.Fatalf("Tab: account %d folder %d", h.u.accountID, h.u.folder.ID)
	}
	h.press(key.NameTab) // round to the first again
	if h.u.accountID != home.id {
		t.Errorf("Tab from the last account: %d", h.u.accountID)
	}
	h.press(key.NameTab, key.ModShift)
	if h.u.accountID != work.id {
		t.Errorf("Shift+Tab: %d", h.u.accountID)
	}
}

// A notification for another account's mail opens it in that account.
func TestShowMessageSwitchesAccount(t *testing.T) {
	h := newHarness(t)
	work := addSecondAccount(t, h)
	h.u.showMessage(work.inbox.ID, 1)
	h.eventually("open", func() bool { return h.u.conv != nil && h.loaded() })
	if h.u.accountID != work.id || h.u.conv.conv.key != "w1" {
		t.Errorf("account %d, conversation %q", h.u.accountID, h.u.conv.conv.key)
	}
}

// An account the daemon hadn't cached when the window opened (its password
// stored later, say) shows up the next time the folder pane opens.
func TestAccountAddedLater(t *testing.T) {
	h := newHarness(t)
	home, err := loadAccount(context.Background(), h.db, h.u.account, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.u.accounts = []*account{home}
	h.u.missing = []*config.Account{{Name: "late", Email: "late@example.org"}}
	h.u.dataDir = t.TempDir()

	h.press("C")
	if got := strings.Join(paneLabels(h), " "); got != "INBOX Sent Archive" {
		t.Fatalf("before: %s", got)
	}
	h.press(key.NameEscape)

	ctx := context.Background()
	id, _ := h.db.EnsureAccount(ctx, "late")
	inbox, _ := h.db.EnsureFolder(ctx, id, "INBOX")
	h.db.SetFolderInfo(ctx, inbox.ID, ".", "")
	h.press("C")
	if got := strings.Join(paneLabels(h), " "); got != "#home INBOX Sent Archive +late(0)" || len(h.u.missing) != 0 {
		t.Errorf("after: %s (missing %d)", got, len(h.u.missing))
	}
}
