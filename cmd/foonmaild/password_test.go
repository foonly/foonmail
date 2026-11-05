package main

import (
	"testing"

	"github.com/zalando/go-keyring"

	"foonmail/internal/config"
)

// Gmail's SMTP host differs from its IMAP host; one keyring entry does.
func TestSMTPUsesIMAPPassword(t *testing.T) {
	keyring.MockInit()
	cfg := &config.Config{Accounts: []config.Account{{
		Name: "gmail",
		IMAP: config.Server{Host: "imap.gmail.com", Port: 993, Username: "kim@gmail.com"},
		SMTP: config.Server{Host: "smtp.gmail.com", Port: 465, Username: "kim@gmail.com"},
	}}}
	pw := passwords(cfg)
	if _, err := pw(cfg.Accounts[0].SMTP); err == nil {
		t.Fatal("found a password before any was stored")
	}
	service, user := cfg.Accounts[0].IMAP.SecretService()
	keyring.Set(service, user, "app-password")
	if p, err := pw(cfg.Accounts[0].SMTP); err != nil || p != "app-password" {
		t.Errorf("SMTP password = %q, %v", p, err)
	}
	// Its own entry wins.
	service, user = cfg.Accounts[0].SMTP.SecretService()
	keyring.Set(service, user, "smtp-only")
	if p, _ := pw(cfg.Accounts[0].SMTP); p != "smtp-only" {
		t.Errorf("SMTP password = %q", p)
	}
}
