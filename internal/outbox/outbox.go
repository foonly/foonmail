// Package outbox sends mail: it keeps messages on disk, holds them for an
// undo window, submits them over SMTP, and copies them to the Sent folder.
//
// A message moves Waiting → Sending → Sent → Done. Undo works only while
// it is Waiting. If foonmaild stops, or the connection breaks, after a
// message was handed to the server, nobody knows whether it was accepted:
// it becomes Uncertain and is never resent without the user saying so,
// since a duplicate to a recipient is worse than asking.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"foonmail/internal/config"
)

const (
	defaultDelay = 10 * time.Second
	maxAttempts  = 8
	keepFinished = 7 * 24 * time.Hour
)

var (
	// ErrTooLate means the message was already being sent when undo came.
	ErrTooLate = errors.New("too late to undo: the message is already being sent")
	// ErrTooLarge means the server's size limit is smaller than the message.
	ErrTooLarge = errors.New("the message is larger than the server accepts")
	// ErrNotFound means there is no such outbox item.
	ErrNotFound = errNoItem
)

// Sender submits a message over SMTP; see SMTP.
type Sender interface {
	Send(ctx context.Context, acct *config.Account, from string, rcpts []string, data []byte) error
}

// Copier stores messages in an account's mail folders.
type Copier interface {
	AppendSent(ctx context.Context, account string, raw []byte) error
	AppendDraft(ctx context.Context, account string, raw []byte) error
}

// Event reports outbox progress, for notifications and the GUI.
type Event struct {
	Kind    EventKind
	ID      int64
	Account string
	Subject string
	To      string
	Err     error // why, for Failed, Retrying, Uncertain and a failed draft copy on Undone
}

type EventKind int

const (
	Queued        EventKind = iota // waiting in the undo window
	SentOK                         // accepted by the server
	Undone                         // undone in time; Err set if saving it as a draft failed
	Retrying                       // a temporary error; it will be tried again
	FailedSend                     // refused for good
	UncertainSend                  // may or may not have been sent; the user decides
)

// Options configures an Outbox.
type Options struct {
	DataDir  string
	Accounts []config.Account
	Sender   Sender
	Copier   Copier
	Notify   func(Event)   // nil: events are dropped
	Delay    time.Duration // undo window; default 10 seconds
	Log      *slog.Logger
	Now      func() time.Time
}

// Outbox sends mail.
type Outbox struct {
	opts     Options
	store    *store
	accounts map[string]*config.Account
	wake     chan struct{}
	log      *slog.Logger
}

// Open opens the outbox in opts.DataDir.
func Open(opts Options) (*Outbox, error) {
	if opts.Delay == 0 {
		opts.Delay = defaultDelay
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	st, err := openStore(filepath.Join(opts.DataDir, "outbox.db"))
	if err != nil {
		return nil, err
	}
	o := &Outbox{opts: opts, store: st, accounts: map[string]*config.Account{},
		wake: make(chan struct{}, 1), log: opts.Log.With("part", "outbox")}
	for i := range opts.Accounts {
		o.accounts[opts.Accounts[i].Name] = &opts.Accounts[i]
	}
	return o, nil
}

// Close closes the outbox database.
func (o *Outbox) Close() error { return o.store.close() }

// RememberSizeLimit records an SMTP server's SIZE limit; see SMTP.OnSizeLimit.
func (o *Outbox) RememberSizeLimit(account string, bytes int) {
	if err := o.store.setSizeLimit(context.Background(), account, bytes); err != nil {
		o.log.Warn("saving the size limit", "err", err)
	}
}

// Submit queues a message from an account. It is sent after the undo
// window. A message over the server's known size limit is refused here,
// before the window starts.
func (o *Outbox) Submit(ctx context.Context, account string, raw []byte) (int64, error) {
	acct := o.accounts[account]
	if acct == nil {
		return 0, fmt.Errorf("no account named %q", account)
	}
	now := o.opts.Now()
	p, err := prepare(raw, acct, now)
	if err != nil {
		return 0, err
	}
	if limit, ok := o.store.sizeLimit(ctx, account); ok && len(p.wire) > limit {
		return 0, fmt.Errorf("%w: %d bytes, the limit is %d", ErrTooLarge, len(p.wire), limit)
	}
	id, err := o.store.add(ctx, account, p, now.Add(o.opts.Delay), now)
	if err != nil {
		return 0, err
	}
	o.log.Info("queued", "id", id, "account", account, "rcpts", len(p.rcpts), "bytes", len(p.wire))
	o.notify(Event{Kind: Queued, ID: id, Account: account, Subject: p.subject, To: p.to})
	o.poke()
	return id, nil
}

// Undo takes a message back if it hasn't started sending, and saves it to
// Drafts (best effort: the result's Err says if that failed).
func (o *Outbox) Undo(ctx context.Context, id int64) error {
	ok, err := o.store.move(ctx, id, Cancelled, Waiting)
	if err != nil {
		return err
	}
	it, gerr := o.store.get(ctx, id)
	if gerr != nil {
		return notFound(gerr)
	}
	if !ok {
		return ErrTooLate
	}
	ev := Event{Kind: Undone, ID: id, Account: it.Account, Subject: it.Subject, To: it.To}
	if o.opts.Copier != nil {
		ev.Err = o.opts.Copier.AppendDraft(ctx, it.Account, it.Stored)
	}
	o.log.Info("undone", "id", id, "draft_err", ev.Err)
	o.notify(ev)
	return nil
}

// Resolve settles an Uncertain message: resend it, or accept that it was
// sent (it is then copied to Sent like any sent message).
func (o *Outbox) Resolve(ctx context.Context, id int64, resend bool) error {
	to := Sent
	if resend {
		to = Waiting
	}
	ok, err := o.store.move(ctx, id, to, Uncertain, Failed)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := o.store.get(ctx, id); err != nil {
			return notFound(err)
		}
		return errors.New("only uncertain or failed messages can be resolved")
	}
	if err := o.store.setOutcome(ctx, id, to, o.opts.Now(), "", false); err != nil {
		return err
	}
	o.poke()
	return nil
}

// List returns the outbox: everything unfinished, and recent finished ones.
func (o *Outbox) List(ctx context.Context) ([]Item, error) { return o.store.list(ctx) }

func (o *Outbox) notify(e Event) {
	if o.opts.Notify != nil {
		o.opts.Notify(e)
	}
}

func (o *Outbox) poke() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Run sends and copies due messages until ctx is cancelled.
func (o *Outbox) Run(ctx context.Context) {
	stuck, err := o.store.recover(ctx)
	if err != nil {
		o.log.Error("recovering the outbox", "err", err)
	}
	for _, it := range stuck {
		o.log.Warn("message may or may not have been sent", "id", it.ID)
		o.notify(Event{Kind: UncertainSend, ID: it.ID, Account: it.Account, Subject: it.Subject, To: it.To,
			Err: errors.New("foonmaild stopped while sending it")})
	}
	_ = o.store.prune(ctx, keepFinished)

	for {
		o.runDue(ctx)
		wait := time.Minute
		if next, ok, err := o.store.nextDue(ctx); err == nil && ok {
			wait = min(wait, max(0, next.Sub(o.opts.Now())))
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-o.wake:
			t.Stop()
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

func (o *Outbox) runDue(ctx context.Context) {
	items, err := o.store.due(ctx, o.opts.Now())
	if err != nil {
		o.log.Error("reading the outbox", "err", err)
		return
	}
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		switch it.State {
		case Waiting:
			o.send(ctx, it)
		case Sent:
			o.copyToSent(ctx, it)
		}
	}
}

func (o *Outbox) send(ctx context.Context, it Item) {
	acct := o.accounts[it.Account]
	if acct == nil {
		o.store.setOutcome(ctx, it.ID, Failed, it.SendAfter, "the account is no longer configured", false)
		return
	}
	// Claim it first, on disk: from here undo is refused, and a crash
	// leaves it Sending, which becomes Uncertain on restart.
	if ok, err := o.store.move(ctx, it.ID, Sending, Waiting); err != nil || !ok {
		return // undone just now, or an error
	}
	ev := Event{ID: it.ID, Account: it.Account, Subject: it.Subject, To: it.To}
	err := o.opts.Sender.Send(ctx, acct, it.From, it.Rcpts, it.Wire)
	now := o.opts.Now()
	switch {
	case err == nil:
		o.store.setOutcome(ctx, it.ID, Sent, now, "", true)
		o.log.Info("sent", "id", it.ID)
		ev.Kind = SentOK
		o.notify(ev)
		it.State = Sent
		o.copyToSent(ctx, it)
	case errors.Is(err, ErrUncertain):
		o.store.setOutcome(ctx, it.ID, Uncertain, now, err.Error(), true)
		o.log.Warn("may or may not have been sent", "id", it.ID, "err", err)
		ev.Kind, ev.Err = UncertainSend, err
		o.notify(ev)
	case errors.Is(err, ErrPermanent) || it.Attempts+1 >= maxAttempts:
		o.store.setOutcome(ctx, it.ID, Failed, now, err.Error(), true)
		o.log.Warn("send failed", "id", it.ID, "err", err)
		ev.Kind, ev.Err = FailedSend, err
		o.notify(ev)
	default:
		next := now.Add(backoff(it.Attempts + 1))
		o.store.setOutcome(ctx, it.ID, Waiting, next, err.Error(), true)
		o.log.Info("will retry", "id", it.ID, "at", next, "err", err)
		ev.Kind, ev.Err = Retrying, err
		o.notify(ev)
	}
}

// copyToSent stores a sent message in the Sent folder, unless the account
// says the server does that itself. Failures are retried; the message is
// already sent either way.
func (o *Outbox) copyToSent(ctx context.Context, it Item) {
	acct := o.accounts[it.Account]
	if acct == nil || !*acct.AppendSent || o.opts.Copier == nil {
		o.store.setOutcome(ctx, it.ID, Done, o.opts.Now(), "", false)
		return
	}
	if err := o.opts.Copier.AppendSent(ctx, it.Account, it.Stored); err != nil {
		next := o.opts.Now().Add(backoff(it.Attempts))
		o.store.setOutcome(ctx, it.ID, Sent, next, "copying to Sent: "+err.Error(), false)
		o.log.Info("copy to Sent will be retried", "id", it.ID, "err", err)
		return
	}
	o.store.setOutcome(ctx, it.ID, Done, o.opts.Now(), "", false)
}

// backoff is the wait before attempt n+1: 30 s, 1, 2, 4, 8, 16, 32 minutes.
func backoff(n int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < n && d < 32*time.Minute; i++ {
		d *= 2
	}
	return d
}
