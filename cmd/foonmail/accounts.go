package main

import (
	"context"
	"fmt"
	"net/mail"
	"path/filepath"
	"slices"
	"strings"

	"foonmail/internal/cache"
	"foonmail/internal/config"
)

// account is a configured account the window can show.
type account struct {
	cfg          *config.Account
	id           int64 // in the cache
	own          []string
	inbox        cache.Folder
	sent         *cache.Folder
	autosavePath string // where a message being written from it is kept
}

// loadAccount finds a configured account in the cache, with its INBOX and
// Sent folders.
func loadAccount(ctx context.Context, db *cache.DB, cfg *config.Account, dataDir string) (*account, error) {
	id, err := db.AccountID(ctx, cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("account %s isn't in the cache yet; is foonmaild running? (%v)", cfg.Name, err)
	}
	folders, err := db.Folders(ctx, id)
	if err != nil {
		return nil, err
	}
	a := &account{cfg: cfg, id: id, own: cfg.Addresses(), autosavePath: filepath.Join(dataDir, "unsent-"+cfg.Name+".json")}
	found := false
	for _, f := range folders {
		if f.Name == "INBOX" {
			a.inbox, found = f, true
		}
		if f.SpecialUse == `\Sent` && a.sent == nil {
			sent := f
			a.sent = &sent
		}
	}
	if !found {
		return nil, fmt.Errorf("account %s: INBOX isn't in the cache yet; is foonmaild running?", cfg.Name)
	}
	return a, nil
}

// useAccount makes a the account shown. The folder is the caller's to set.
func (u *ui) useAccount(a *account) {
	if a.id == u.accountID && u.account == a.cfg {
		return
	}
	u.account, u.accountID, u.own, u.sent, u.autosavePath = a.cfg, a.id, a.own, a.sent, a.autosavePath
	u.contacts = nil
	u.status = nil
	u.refreshStatus()
}

// accountByID is the loaded account with a cache id, or nil.
func (u *ui) accountByID(id int64) *account {
	for _, a := range u.accounts {
		if a.id == id {
			return a
		}
	}
	return nil
}

// accountFor picks the account a message is sent from by its From
// address, so changing From to another account's address sends through
// that account. fallback is used when no account has the address.
func (u *ui) accountFor(from string, fallback *config.Account) *config.Account {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return fallback
	}
	want := strings.ToLower(addr.Address)
	if slices.Contains(fallback.Addresses(), want) {
		return fallback
	}
	for _, a := range u.accounts {
		if slices.Contains(a.own, want) {
			return a.cfg
		}
	}
	return fallback
}

// loadMissing adds the configured accounts that have reached the cache
// since the window opened, such as one whose password was just stored.
func (u *ui) loadMissing() {
	still := u.missing[:0]
	for _, cfg := range u.missing {
		a, err := loadAccount(context.Background(), u.db, cfg, u.dataDir)
		if err != nil {
			still = append(still, cfg)
			continue
		}
		u.accounts = append(u.accounts, a)
	}
	u.missing = still
}
