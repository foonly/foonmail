// Package imapsync keeps the cache in step with an account's IMAP server.
//
// Each account runs two connections:
//
//   - The INBOX connection sits in IDLE, applies new mail, flag changes and
//     expunges to the cache as they happen, and reports new mail.
//   - The folder connection lists folders, keeps the cache's folder list
//     current, and polls the configured watched folders with STATUS, syncing
//     a folder only when STATUS shows it changed. (Syncing a folder the GUI
//     opens will use this connection too, so it never interrupts IDLE.)
//
// Both reconnect with backoff. When woken (see Waker: resume from sleep,
// the network coming up, or a manual sync), both drop their connection, or
// stop waiting to retry, and reconnect and resync at once.
package imapsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/hdr"
)

const (
	commandTimeout       = 30 * time.Second
	searchTimeout        = 5 * time.Minute // a body search reads the whole folder
	defaultCheckInterval = 5 * time.Minute
	maxBackoff           = time.Minute
)

// Options configures a Syncer.
type Options struct {
	Account *config.Account
	DB      *cache.DB
	Log     *slog.Logger // nil: slog.Default()
	// Password returns the password for a server, usually from the keyring.
	Password func(config.Server) (string, error)
	// Notify receives events. It is called from the sync goroutines and
	// must not block for long. nil: events are dropped.
	Notify func(Event)
	// CheckInterval is how often an idle INBOX connection is verified with
	// NOOP. Default: 5 minutes.
	CheckInterval time.Duration
	// Dial connects to a server. nil: TLS or STARTTLS as configured.
	Dial func(s config.Server, opts *imapclient.Options) (*imapclient.Client, error)
}

// Event is something the daemon may want to tell the user about.
type Event interface{ isEvent() }

// NewMail is a message that arrived in INBOX. It is not sent for messages
// found by the first sync after the program starts.
type NewMail struct {
	Account  string
	UID      uint32
	Envelope cache.Envelope
	Seen     bool // already read elsewhere, such as on the phone
}

// UnreadChanged reports INBOX's unread count after a change.
type UnreadChanged struct {
	Account string
	Unread  int
}

// ConnectionChanged reports whether the INBOX connection is up.
type ConnectionChanged struct {
	Account string
	Online  bool
	Err     error // why it went down; nil when Online
}

func (NewMail) isEvent()           {}
func (UnreadChanged) isEvent()     {}
func (ConnectionChanged) isEvent() {}

var errWoken = errors.New("woken to reconnect")

// Syncer syncs one account.
type Syncer struct {
	opts      Options
	acct      *config.Account
	db        *cache.DB
	log       *slog.Logger
	accountID int64

	mu               sync.Mutex
	baselineValidity uint32 // UIDVALIDITY the baseline belongs to
	baselineUID      uint32 // INBOX UIDs below this were there at start; see setBaseline

	requests chan request // served by the folder connection

	firstBackoff time.Duration // the first wait to retry; tests make it long
}

// New prepares a Syncer for an account.
func New(ctx context.Context, opts Options) (*Syncer, error) {
	if opts.Account == nil || opts.DB == nil || opts.Password == nil {
		return nil, errors.New("imapsync: Account, DB and Password are required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.CheckInterval == 0 {
		opts.CheckInterval = defaultCheckInterval
	}
	id, err := opts.DB.EnsureAccount(ctx, opts.Account.Name)
	if err != nil {
		return nil, err
	}
	return &Syncer{
		opts:      opts,
		acct:      opts.Account,
		db:        opts.DB,
		log:       opts.Log.With("account", opts.Account.Name),
		accountID: id,
		requests:  make(chan request),

		firstBackoff: time.Second,
	}, nil
}

// Run syncs until ctx is cancelled. A value on a wake channel makes that
// connection reconnect at once; each needs its own (see Waker.Subscribe).
func (s *Syncer) Run(ctx context.Context, inboxWake, folderWake <-chan struct{}) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.loop(ctx, "inbox", inboxWake, nil, s.inboxSession)
	}()
	go func() {
		defer wg.Done()
		s.loop(ctx, "folders", folderWake, s.requests, s.folderSession)
	}()
	wg.Wait()
}

// loop runs session until ctx is cancelled, reconnecting with backoff.
// serve, if not nil, receives requests to answer with ErrOffline while the
// connection is down.
func (s *Syncer) loop(ctx context.Context, name string, wake <-chan struct{}, serve <-chan request,
	session func(context.Context, <-chan struct{}) error) {
	log := s.log.With("conn", name)
	backoff := s.firstBackoff
retryNow:
	for {
		// A wake from before this attempt is answered by it.
		select {
		case <-wake:
		default:
		}
		started := time.Now()
		err := session(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errWoken) {
			// Not reported as offline: it reconnects at once, and a
			// failure to will be.
			log.Info("woken, reconnecting")
			backoff = s.firstBackoff
			continue
		}
		if name == "inbox" {
			s.notify(ConnectionChanged{Account: s.acct.Name, Online: false, Err: err})
		}
		if time.Since(started) > time.Minute {
			backoff = s.firstBackoff
		}
		log.Warn("disconnected", "err", err, "retry_in", backoff)
		// While waiting, answer requests at once instead of leaving them hanging.
		retry := time.NewTimer(backoff)
	wait:
		for {
			select {
			case <-retry.C:
				break wait
			case <-wake:
				// The network is back, or the user asked: retry now.
				retry.Stop()
				log.Info("woken, retrying now")
				backoff = s.firstBackoff
				continue retryNow
			case req := <-serve:
				req.reply <- reqResult{err: fmt.Errorf("%w: %v", ErrOffline, err)}
			case <-ctx.Done():
				retry.Stop()
				return
			}
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (s *Syncer) notify(e Event) {
	if s.opts.Notify != nil {
		s.opts.Notify(e)
	}
}

// connect dials and logs in.
func (s *Syncer) connect(handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
	opts := &imapclient.Options{
		Dialer:                &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		UnilateralDataHandler: handler,
		WordDecoder:           hdr.Decoder,
	}
	pass, err := s.opts.Password(s.acct.IMAP)
	if err != nil {
		return nil, fmt.Errorf("password: %w", err)
	}
	dial := s.opts.Dial
	if dial == nil {
		dial = dialServer
	}
	c, err := dial(s.acct.IMAP, opts)
	if err != nil {
		return nil, err
	}
	if err := do(c, "login", func() error { return c.Login(s.acct.IMAP.Username, pass).Wait() }); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func dialServer(srv config.Server, opts *imapclient.Options) (*imapclient.Client, error) {
	if srv.Security == config.SecurityStartTLS {
		return imapclient.DialStartTLS(srv.Address(), opts)
	}
	return imapclient.DialTLS(srv.Address(), opts)
}

// do runs an IMAP command and gives up after commandTimeout, closing the
// connection so the blocked command returns.
func do(c *imapclient.Client, what string, f func() error) error {
	return doWithin(c, what, commandTimeout, f)
}

// doWithin is do with another timeout.
func doWithin(c *imapclient.Client, what string, timeout time.Duration, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	case <-t.C:
		c.Close()
		return fmt.Errorf("%s: no response in %s", what, timeout)
	}
}

// selectFolder selects a mailbox, read-only unless it is the live INBOX.
func selectFolder(c *imapclient.Client, name string, readOnly, condstore bool) (*imap.SelectData, error) {
	var sel *imap.SelectData
	err := do(c, "select "+name, func() (err error) {
		sel, err = c.Select(name, &imap.SelectOptions{ReadOnly: readOnly, CondStore: condstore}).Wait()
		return err
	})
	return sel, err
}
