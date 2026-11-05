// Package secrets keeps the servers' passwords in the Secret Service
// keyring (GNOME Keyring, KWallet, KeePassXC…): looking them up for the
// daemon, listing which are missing, and checking and storing new ones.
//
// A password is stored under service "foonmail/<host>" and the server's
// username. An SMTP server on another host than its account's IMAP server
// but with the same username, as with Gmail, uses the IMAP password when
// it has no entry of its own, so one entry does for both.
package secrets

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"foonmail/internal/config"
)

// ErrMissing is returned when no password is stored for a server.
var ErrMissing = errors.New("no password in the keyring")

// get reads one server's own entry.
func get(s config.Server) (string, error) {
	service, user := s.SecretService()
	p, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrMissing
	}
	return p, err
}

// Store saves a server's password in the keyring.
func Store(s config.Server, password string) error {
	service, user := s.SecretService()
	return keyring.Set(service, user, password)
}

// Lookup returns the password for a server of one of cfg's accounts.
func Lookup(cfg *config.Config, s config.Server) (string, error) {
	p, err := get(s)
	if !errors.Is(err, ErrMissing) {
		return p, err
	}
	if imap, ok := sharedWith(cfg, s); ok {
		if p, err := get(imap); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w for %s on %s; run: foonmailctl passwords", ErrMissing, s.Username, s.Host)
}

// sharedWith finds the IMAP server whose password an SMTP server falls
// back to.
func sharedWith(cfg *config.Config, s config.Server) (config.Server, bool) {
	for _, a := range cfg.Accounts {
		if a.SMTP == s && a.IMAP.Host != s.Host && a.IMAP.Username == s.Username {
			return a.IMAP, true
		}
	}
	return config.Server{}, false
}

// State is whether a password is there.
type State int

const (
	Stored  State = iota // in the keyring
	Missing              // not in the keyring
	Shared               // an SMTP server using its account's IMAP password, stored or not
	Unknown              // the keyring couldn't be read
)

func (s State) String() string {
	return [...]string{"stored", "missing", "uses the IMAP password", "keyring error"}[s]
}

// Need is a password an account needs.
type Need struct {
	Account string
	Role    string // "IMAP" or "SMTP"
	Server  config.Server
	State   State
	Err     error // for Unknown
}

// Needs lists the passwords cfg's accounts need, each keyring entry once:
// an SMTP server on the IMAP host with the IMAP username shares its entry
// and isn't listed.
func Needs(cfg *config.Config) []Need {
	var out []Need
	seen := map[[2]string]bool{}
	add := func(account, role string, s config.Server) {
		service, user := s.SecretService()
		if seen[[2]string{service, user}] {
			return
		}
		seen[[2]string{service, user}] = true
		n := Need{Account: account, Role: role, Server: s}
		switch _, err := get(s); {
		case err == nil:
			n.State = Stored
		case errors.Is(err, ErrMissing):
			n.State = Missing
			if _, ok := sharedWith(cfg, s); ok && role == "SMTP" {
				n.State = Shared // listed with its IMAP server, which may be missing
			}
		default:
			n.State, n.Err = Unknown, err
		}
		out = append(out, n)
	}
	for _, a := range cfg.Accounts {
		add(a.Name, "IMAP", a.IMAP)
	}
	for _, a := range cfg.Accounts {
		add(a.Name, "SMTP", a.SMTP)
	}
	return out
}
