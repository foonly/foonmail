package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `
[[account]]
name = "uplink"
email = "Niklas <Niklas@Uplink.fi>"
aliases = ["postmaster@uplink.fi"]

[account.imap]
host = "mail.uplink.fi"
username = "niklas"
`

func TestDefaults(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	c, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.CacheDir != "/tmp/xdg-cache/foonmail" {
		t.Errorf("CacheDir = %q", c.CacheDir)
	}
	a := c.Accounts[0]
	if a.IMAP.Address() != "mail.uplink.fi:993" || a.IMAP.Security != SecurityTLS {
		t.Errorf("IMAP = %+v", a.IMAP)
	}
	if a.SMTP.Address() != "mail.uplink.fi:465" || a.SMTP.Username != "niklas" {
		t.Errorf("SMTP = %+v, want IMAP host and username on 465", a.SMTP)
	}
	if !*a.AppendSent || !*a.NotifyDetails {
		t.Error("append_sent and notify_details should default to true")
	}
	if !slices.Equal(a.Folders.Watched, []string{"@sent"}) || a.Folders.PollInterval != 5*time.Minute {
		t.Errorf("Folders = %+v", a.Folders)
	}
	if got := a.Addresses(); !slices.Equal(got, []string{"niklas@uplink.fi", "postmaster@uplink.fi"}) {
		t.Errorf("Addresses = %v", got)
	}
	if svc, user := a.IMAP.SecretService(); svc != "foonmail/mail.uplink.fi" || user != "niklas" {
		t.Errorf("SecretService = %q, %q", svc, user)
	}
}

func TestStartTLSPorts(t *testing.T) {
	c, err := Load(write(t, minimal+`
security = "starttls"
[account.smtp]
security = "starttls"
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Accounts[0].IMAP.Port != 143 || c.Accounts[0].SMTP.Port != 587 {
		t.Errorf("ports = %d, %d; want 143, 587", c.Accounts[0].IMAP.Port, c.Accounts[0].SMTP.Port)
	}
}

func TestEmptyWatchedStaysEmpty(t *testing.T) {
	c, err := Load(write(t, minimal+"\n[account.folders]\nwatched = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Accounts[0].Folders.Watched) != 0 {
		t.Errorf("watched = %v; an explicit empty list must not get the default", c.Accounts[0].Folders.Watched)
	}
}

func TestProblemsAreAllReported(t *testing.T) {
	_, err := Load(write(t, `
cache_dir = "relative/dir"

[[account]]
name = "Bad Name"
email = "not an address"
pasword = "oops"

[account.imap]
host = "mail.example.com"
security = "ssl"
port = 70000

[account.folders]
watched = ["@sent", "@outbox", "INBOX", "@sent"]
poll_interval = "5s"

[[account]]
name = "dup"
email = "a@example.com"
[account.imap]
username = "a"

[[account]]
name = "dup"
email = "b@example.com"
[account.imap]
host = "h"
username = "b"
`))
	var cerr *Error
	if !errors.As(err, &cerr) {
		t.Fatalf("err = %v, want *Error", err)
	}
	want := []string{
		`unknown setting "account.pasword"`,
		"cache_dir must be an absolute path",
		`account "Bad Name": name may only contain`,
		`email "not an address" is not a valid address`,
		"imap.username is required",
		`imap.security must be "tls" or "starttls", not "ssl"`,
		"imap.port 70000 is out of range",
		`unknown special folder "@outbox"`,
		"INBOX is always watched live",
		`lists "@sent" twice`,
		"poll_interval must be at least 30s",
		`account "dup": imap.host is required`,
		`account "dup": name is used by more than one account`,
	}
	msg := err.Error()
	for _, w := range want {
		if !strings.Contains(msg, w) {
			t.Errorf("missing problem %q in:\n%s", w, msg)
		}
	}
}

func TestNoAccounts(t *testing.T) {
	_, err := Load(write(t, ""))
	if err == nil || !strings.Contains(err.Error(), "no accounts") {
		t.Errorf("err = %v", err)
	}
}

func TestExampleFileLoads(t *testing.T) {
	if _, err := Load("../../config.example.toml"); err != nil {
		t.Errorf("config.example.toml: %v", err)
	}
}

func TestPathOverride(t *testing.T) {
	t.Setenv("FOONMAIL_CONFIG", "/somewhere/else.toml")
	if p, _ := Path(); p != "/somewhere/else.toml" {
		t.Errorf("Path = %q", p)
	}
}
