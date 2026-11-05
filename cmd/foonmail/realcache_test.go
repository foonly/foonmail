package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foonmail/internal/cache"
)

// TestRealCacheGrouping times loading and grouping a real cache's INBOX and
// Sent. It runs only when FOONMAIL_TEST_CACHE points at a mail.db, set in
// the environment or in the gitignored local.test.env.
func TestRealCacheGrouping(t *testing.T) {
	loadLocalTestEnv(t)
	path := os.Getenv("FOONMAIL_TEST_CACHE")
	if path == "" {
		t.Skip("set FOONMAIL_TEST_CACHE in local.test.env (see local.test.env.example)")
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

// loadLocalTestEnv sets variables from local.test.env in the repository
// root, unless they are already set. The file is gitignored: it holds
// what tests against your own mail need, such as your account name.
func loadLocalTestEnv(t *testing.T) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
	data, err := os.ReadFile(filepath.Join(dir, "local.test.env"))
	if err != nil {
		return
	}
	home, _ := os.UserHomeDir()
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") || os.Getenv(key) != "" {
			continue
		}
		if strings.HasPrefix(value, "~/") {
			value = filepath.Join(home, value[2:])
		}
		t.Setenv(strings.TrimSpace(key), strings.TrimSpace(value))
	}
}
