package secrets

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"foonmail/internal/config"
)

func gmailAndHome() *config.Config {
	return &config.Config{Accounts: []config.Account{
		{
			Name: "home",
			IMAP: config.Server{Host: "mail.example.org", Port: 993, Username: "me"},
			SMTP: config.Server{Host: "mail.example.org", Port: 465, Username: "me"},
		},
		{
			Name: "gmail",
			IMAP: config.Server{Host: "imap.gmail.com", Port: 993, Username: "kim@gmail.com"},
			SMTP: config.Server{Host: "smtp.gmail.com", Port: 465, Username: "kim@gmail.com"},
		},
	}}
}

// Gmail's SMTP host differs from its IMAP host; one keyring entry does.
func TestSMTPUsesIMAPPassword(t *testing.T) {
	keyring.MockInit()
	cfg := gmailAndHome()
	gmail := cfg.Accounts[1]
	if _, err := Lookup(cfg, gmail.SMTP); !errors.Is(err, ErrMissing) {
		t.Fatalf("before storing: %v", err)
	}
	Store(gmail.IMAP, "app-password")
	if p, err := Lookup(cfg, gmail.SMTP); err != nil || p != "app-password" {
		t.Errorf("SMTP password = %q, %v", p, err)
	}
	// Its own entry wins.
	Store(gmail.SMTP, "smtp-only")
	if p, _ := Lookup(cfg, gmail.SMTP); p != "smtp-only" {
		t.Errorf("SMTP password = %q", p)
	}
}

func TestNeeds(t *testing.T) {
	keyring.MockInit()
	cfg := gmailAndHome()
	describe := func() string {
		var s string
		for _, n := range Needs(cfg) {
			s += n.Account + " " + n.Role + " " + n.State.String() + "; "
		}
		return s
	}
	// home's SMTP is the same entry as its IMAP, so it isn't listed.
	if got, want := describe(), "home IMAP missing; gmail IMAP missing; gmail SMTP uses the IMAP password; "; got != want {
		t.Errorf("nothing stored:\n got %s\nwant %s", got, want)
	}
	Store(cfg.Accounts[0].IMAP, "x")
	Store(cfg.Accounts[1].IMAP, "y")
	if got, want := describe(), "home IMAP stored; gmail IMAP stored; gmail SMTP uses the IMAP password; "; got != want {
		t.Errorf("stored:\n got %s\nwant %s", got, want)
	}
}
