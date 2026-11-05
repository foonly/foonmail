package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"foonmail/internal/config"
	"foonmail/internal/secrets"
)

func testTool(answers ...string) (*passwordTool, *strings.Builder, *[]string) {
	keyring.MockInit()
	cfg := &config.Config{Accounts: []config.Account{
		{Name: "home", IMAP: config.Server{Host: "mail.example.org", Username: "me"}, SMTP: config.Server{Host: "mail.example.org", Username: "me"}},
		{Name: "gmail", IMAP: config.Server{Host: "imap.gmail.com", Username: "kim@gmail.com"}, SMTP: config.Server{Host: "smtp.gmail.com", Username: "kim@gmail.com"}},
	}}
	out := &strings.Builder{}
	var logins []string
	t := &passwordTool{
		cfg: cfg,
		out: out,
		read: func(string) (string, error) {
			if len(answers) == 0 {
				return "", errors.New("asked once too often")
			}
			a := answers[0]
			answers = answers[1:]
			return a, nil
		},
		verify: func(_ context.Context, role string, s config.Server, p string) error {
			logins = append(logins, role+" "+s.Host+" "+p)
			if strings.HasPrefix(p, "wrong") {
				return errors.New("authentication failed")
			}
			return nil
		},
		store: secrets.Store,
	}
	return t, out, &logins
}

func stored(t *testing.T, cfg *config.Config, s config.Server) string {
	t.Helper()
	p, err := secrets.Lookup(cfg, s)
	if err != nil {
		return ""
	}
	return p
}

func TestPasswordsAsksForMissingOnes(t *testing.T) {
	tool, out, logins := testTool("wrong1", "home-pw", "gmail-pw")
	if err := tool.run(context.Background(), false, ""); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg := tool.cfg
	if stored(t, cfg, cfg.Accounts[0].IMAP) != "home-pw" || stored(t, cfg, cfg.Accounts[1].IMAP) != "gmail-pw" {
		t.Errorf("stored:\n%s", out)
	}
	// Gmail's SMTP needed no password of its own.
	if stored(t, cfg, cfg.Accounts[1].SMTP) != "gmail-pw" {
		t.Error("gmail SMTP doesn't find the IMAP password")
	}
	if got := strings.Join(*logins, "; "); got != "IMAP mail.example.org wrong1; IMAP mail.example.org home-pw; IMAP imap.gmail.com gmail-pw" {
		t.Errorf("logins: %s", got)
	}
	if !strings.Contains(out.String(), "failed: authentication failed") {
		t.Errorf("a failed login isn't reported:\n%s", out)
	}

	// Nothing missing now: nothing is asked.
	tool.read = func(string) (string, error) { return "", errors.New("asked") }
	out.Reset()
	if err := tool.run(context.Background(), false, ""); err != nil || !strings.Contains(out.String(), "Every password is in the keyring.") {
		t.Errorf("%v\n%s", err, out)
	}
}

func TestPasswordsSkipAndGiveUp(t *testing.T) {
	tool, out, _ := testTool("", "wrong", "wrong", "wrong")
	err := tool.run(context.Background(), false, "")
	if err == nil || !strings.Contains(err.Error(), "2 password(s) still missing") {
		t.Errorf("err %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Skipped.") || !strings.Contains(out.String(), "Giving up") {
		t.Errorf("output:\n%s", out)
	}
	if stored(t, tool.cfg, tool.cfg.Accounts[1].IMAP) != "" {
		t.Error("a password that failed to log in was stored")
	}
}

func TestPasswordsChange(t *testing.T) {
	tool, out, _ := testTool("new-pw")
	secrets.Store(tool.cfg.Accounts[0].IMAP, "old")
	secrets.Store(tool.cfg.Accounts[1].IMAP, "kept")
	if err := tool.run(context.Background(), false, "home"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if stored(t, tool.cfg, tool.cfg.Accounts[0].IMAP) != "new-pw" || stored(t, tool.cfg, tool.cfg.Accounts[1].IMAP) != "kept" {
		t.Errorf("after -change home:\n%s", out)
	}
	if err := tool.run(context.Background(), false, "nosuch"); err == nil {
		t.Error("-change with an unknown account")
	}
}

func TestPasswordsNeedATerminal(t *testing.T) {
	tool, _, _ := testTool()
	tool.interactive = func() bool { return false }
	if err := tool.run(context.Background(), false, ""); err == nil || !strings.Contains(err.Error(), "in a terminal") {
		t.Errorf("err %v", err)
	}
}
