package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"foonmail/internal/cache"
)

// TestRealCacheGrouping times loading and grouping a real cache's INBOX and
// Sent. It runs only when FOONMAIL_TEST_CACHE points at a mail.db.
func TestRealCacheGrouping(t *testing.T) {
	path := os.Getenv("FOONMAIL_TEST_CACHE")
	if path == "" {
		t.Skip("set FOONMAIL_TEST_CACHE to a mail.db")
	}
	db, err := cache.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	acct, err := db.AccountID(ctx, os.Getenv("FOONMAIL_TEST_ACCOUNT"))
	if err != nil {
		t.Fatal(err)
	}
	folders, _ := db.Folders(ctx, acct)
	var inbox, sent int64
	for _, f := range folders {
		if f.Name == "INBOX" {
			inbox = f.ID
		}
		if f.SpecialUse == `\Sent` {
			sent = f.ID
		}
	}
	counts, _ := db.FolderCounts(ctx, acct)
	var order []string
	for _, r := range folderRows(folders, counts, time.Now().Year()) {
		order = append(order, strings.Repeat("  ", r.depth)+r.label)
	}
	t.Logf("folder order:\n%s", strings.Join(order, "\n"))

	start := time.Now()
	entries, _ := db.Entries(ctx, inbox)
	more, _ := db.Entries(ctx, sent)
	entries = append(entries, more...)
	loaded := time.Since(start)
	convs := buildConversations(entries, inbox, nil)
	grouped := time.Since(start) - loaded
	multi := 0
	for _, c := range convs {
		if len(c.members) > 1 {
			multi++
		}
		seen := map[string]bool{}
		for _, m := range c.members {
			if m.MessageID != "" && seen[m.MessageID] {
				t.Errorf("conversation %q shows message %q twice", c.subject, m.MessageID)
			}
			seen[m.MessageID] = true
		}
	}
	t.Logf("%d messages loaded in %s, grouped into %d conversations (%d with several messages) in %s",
		len(entries), loaded.Round(time.Millisecond), len(convs), multi, grouped.Round(time.Millisecond))
}
