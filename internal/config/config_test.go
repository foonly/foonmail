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
name = "home"
email = "Kim <Me@Example.org>"
aliases = ["postmaster@example.org"]

[account.imap]
host = "mail.example.org"
username = "me"
`

func TestDefaults(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
	c, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.CacheDir != "/tmp/xdg-cache/foonmail" {
		t.Errorf("CacheDir = %q", c.CacheDir)
	}
	if c.DataDir != "/tmp/xdg-data/foonmail" {
		t.Errorf("DataDir = %q", c.DataDir)
	}
	a := c.Accounts[0]
	if a.IMAP.Address() != "mail.example.org:993" || a.IMAP.Security != SecurityTLS {
		t.Errorf("IMAP = %+v", a.IMAP)
	}
	if a.SMTP.Address() != "mail.example.org:465" || a.SMTP.Username != "me" {
		t.Errorf("SMTP = %+v, want IMAP host and username on 465", a.SMTP)
	}
	if !*a.AppendSent || !*a.NotifyDetails {
		t.Error("append_sent and notify_details should default to true")
	}
	if !slices.Equal(a.Folders.Watched, []string{"@sent"}) || a.Folders.PollInterval != 5*time.Minute {
		t.Errorf("Folders = %+v", a.Folders)
	}
	if got := a.Addresses(); !slices.Equal(got, []string{"me@example.org", "postmaster@example.org"}) {
		t.Errorf("Addresses = %v", got)
	}
	if svc, user := a.IMAP.SecretService(); svc != "foonmail/mail.example.org" || user != "me" {
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

func TestSignatureSetting(t *testing.T) {
	example, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	// The example's signature, uncommented.
	s := strings.NewReplacer(`# signature = """`, `signature = """`, "# Your Name\n", "Your Name\n",
		"# example.com\n", "example.com\n", "# \"\"\"\n", "\"\"\"\n").Replace(string(example))
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Accounts[0].Signature; got != "Your Name\nexample.com\n" {
		t.Errorf("signature %q", got)
	}
}

func TestGmailDefaults(t *testing.T) {
	c, err := Load(write(t, `
[[account]]
name = "gmail"
email = "kim@gmail.com"
[account.imap]
host = "imap.gmail.com"
username = "kim@gmail.com"
[account.smtp]
host = "smtp.gmail.com"

[[account]]
name = "home"
email = "me@example.org"
[account.imap]
host = "mail.example.org"
username = "me"
`))
	if err != nil {
		t.Fatal(err)
	}
	g, h := c.Accounts[0], c.Accounts[1]
	if !g.IsGmail() || *g.AppendSent || g.SMTP.Username != "kim@gmail.com" || g.SMTP.Port != 465 {
		t.Errorf("gmail: %+v", g)
	}
	if h.IsGmail() || !*h.AppendSent {
		t.Errorf("home: %+v", h)
	}
}

func TestDomainAliases(t *testing.T) {
	c, err := Load(write(t, `
[[account]]
name = "home"
email = "me@example.org"
aliases = ["Kim.Strom@Example.org", "@Example.DEV"]
[account.imap]
host = "mail.example.org"
username = "me"
`))
	if err != nil {
		t.Fatal(err)
	}
	own := c.Accounts[0].Addresses()
	if got := strings.Join(own, " "); got != "me@example.org kim.strom@example.org @example.dev" {
		t.Errorf("Addresses = %s", got)
	}
	for addr, want := range map[string]bool{
		"me@example.org": true, "KIM.STROM@example.org": true, "anything@example.dev": true, "Shop@EXAMPLE.dev": true,
		"other@example.org": false, "me@notexample.dev": false, "example.dev": false, "": false,
	} {
		if got := IsOwn(own, addr); got != want {
			t.Errorf("IsOwn(%q) = %v", addr, got)
		}
	}

	_, err = Load(write(t, `
[[account]]
name = "home"
email = "me@example.org"
aliases = ["@", "@nodot", "not an address"]
[account.imap]
host = "mail.example.org"
username = "me"
`))
	if err == nil || strings.Count(err.Error(), "is not a valid address or @domain") != 3 {
		t.Errorf("bad aliases: %v", err)
	}
}

func TestUndoDelay(t *testing.T) {
	account := `
[[account]]
name = "home"
email = "me@example.org"
[account.imap]
host = "mail.example.org"
username = "me"
`
	c, err := Load(write(t, account))
	if err != nil || c.UndoDelay != 5*time.Second {
		t.Errorf("default: %v %v", c, err)
	}
	c, err = Load(write(t, "undo_delay = \"20s\"\n"+account))
	if err != nil || c.UndoDelay != 20*time.Second {
		t.Errorf("20s: %v %v", c.UndoDelay, err)
	}
	for _, bad := range []string{"500ms", "10m"} {
		if _, err := Load(write(t, "undo_delay = \""+bad+"\"\n"+account)); err == nil || !strings.Contains(err.Error(), "undo_delay must be between") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if Seconds(5*time.Second) != "5 seconds" || Seconds(time.Second) != "1 second" {
		t.Error("Seconds")
	}
}
