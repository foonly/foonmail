package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"

	"foonmail/internal/config"
	"foonmail/internal/rpc"
	"foonmail/internal/sieve"
)

// Mail filters live on the server as a Sieve script named "foonmail",
// read and written over ManageSieve. The daemon only carries the script;
// the GUI turns it into rules and back (sieve.Script and sieve.Parse).

// errNoFilters means the account has no ManageSieve server.
var errNoFilters = errors.New("this account has no server-side filters")

// filterStore reads and writes an account's filter script.
type filterStore interface {
	// GetFilters returns foonmail's script ("" if there is none yet) and
	// the name of another script that is active instead, if one is.
	GetFilters(ctx context.Context, account string) (script, otherActive string, err error)
	// PutFilters stores foonmail's script, after the server checks it,
	// and makes it the active one.
	PutFilters(ctx context.Context, account, script string) error
}

// sieveFilters is filterStore over ManageSieve.
type sieveFilters struct {
	cfg      *config.Config
	password func(config.Server) (string, error)
}

func (f *sieveFilters) dial(account string) (*sieve.Client, error) {
	a, err := f.cfg.Account(account)
	if err != nil {
		return nil, err
	}
	addr := a.SieveAddress()
	if addr == "" {
		return nil, errNoFilters
	}
	pass, err := f.password(a.IMAP)
	if err != nil {
		return nil, fmt.Errorf("password: %w", err)
	}
	c, err := sieve.Dial(addr, a.IMAP.Username, pass, nil)
	if err != nil {
		return nil, fmt.Errorf("filters at %s: %w", addr, err)
	}
	return c, nil
}

func (f *sieveFilters) GetFilters(ctx context.Context, account string) (string, string, error) {
	c, err := f.dial(account)
	if err != nil {
		return "", "", err
	}
	defer c.Close()
	list, err := c.ListScripts()
	if err != nil {
		return "", "", err
	}
	script, other := "", ""
	for _, s := range list {
		if s.Name == sieve.ScriptName {
			if script, err = c.GetScript(s.Name); err != nil {
				return "", "", err
			}
		} else if s.Active {
			other = s.Name
		}
	}
	return script, other, nil
}

func (f *sieveFilters) PutFilters(ctx context.Context, account, script string) error {
	c, err := f.dial(account)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.PutScript(sieve.ScriptName, script); err != nil {
		return err
	}
	return c.SetActive(sieve.ScriptName)
}

// GetFilters returns an account's filter script and the name of another
// active script, if any.
func (m methods) GetFilters(account string) (string, string, *dbus.Error) {
	if m.s.filters == nil {
		return "", "", dbusError(errNoFilters)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	script, other, err := m.s.filters.GetFilters(ctx, account)
	return script, other, filterError(err)
}

// PutFilters stores an account's filter script and activates it.
func (m methods) PutFilters(account, script string) *dbus.Error {
	if m.s.filters == nil {
		return dbusError(errNoFilters)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	err := m.s.filters.PutFilters(ctx, account, script)
	if err == nil {
		m.s.log.Info("filters saved", "account", account)
	}
	return filterError(err)
}

func filterError(err error) *dbus.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNoFilters):
		return &dbus.Error{Name: rpc.ErrNameUnsupported, Body: []any{err.Error()}}
	case errors.Is(err, sieve.ErrRejected):
		// The server's reason, such as a script error, on one line.
		msg := strings.Join(strings.Fields(strings.TrimPrefix(err.Error(), sieve.ErrRejected.Error()+": ")), " ")
		return &dbus.Error{Name: rpc.ErrNameRejected, Body: []any{msg}}
	}
	return dbusError(err)
}
