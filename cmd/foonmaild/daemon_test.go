package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"foonmail/internal/cache"
	"foonmail/internal/imapsync"
)

func TestShouldNotify(t *testing.T) {
	own := []string{"me@example.org", "postmaster@example.org"}
	mail := func(from string, seen bool) imapsync.NewMail {
		return imapsync.NewMail{Seen: seen, Envelope: cache.Envelope{From: cache.Address{Addr: from}}}
	}
	cases := []struct {
		ev     imapsync.NewMail
		want   bool
		reason string
	}{
		{mail("bob@example.com", false), true, ""},
		{mail("bob@example.com", true), false, "already read"},
		{mail("Me@Example.ORG", false), false, "from you"},
		{mail("postmaster@example.org", false), false, "from you"},
	}
	for _, c := range cases {
		if got, reason := shouldNotify(own, c.ev); got != c.want || reason != c.reason {
			t.Errorf("shouldNotify(%s, seen=%v) = %v %q, want %v %q",
				c.ev.Envelope.From.Addr, c.ev.Seen, got, reason, c.want, c.reason)
		}
	}
}

func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	f, err := acquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	// flock is per open file description, so a second acquire in this same
	// process conflicts just like another process would.
	if _, err := acquireLock(dir); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second lock: err = %v, want 'already using'", err)
	}
	f.Close()
	g, err := acquireLock(dir)
	if err != nil {
		t.Errorf("lock not released on close: %v", err)
	}
	g.Close()
}

// TestMissingConfig runs the built daemon against a path that doesn't exist.
func TestMissingConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := t.TempDir() + "/foonmaild"
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "-config", "/nonexistent/config.toml").CombinedOutput()
	if err == nil {
		t.Fatal("daemon started without a config")
	}
	if !strings.Contains(string(out), "copy config.example.toml") {
		t.Errorf("unhelpful error: %s", out)
	}
	_ = os.Remove(bin)
}
