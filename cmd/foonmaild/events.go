package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/imapsync"
	"foonmail/internal/notify"
	"foonmail/internal/rpc"
	"foonmail/internal/tray"
)

const (
	// New mail arriving within burstDelay is gathered into one round of
	// notifications; burstThreshold or more become a single summary.
	burstDelay     = 1500 * time.Millisecond
	burstThreshold = 3
	summaryLines   = 3
)

// notifier and trayIcon are what the hub needs from notify.Client and
// tray.Tray, so tests can use fakes. Either may be nil when the desktop has
// no notification server or tray host.
type notifier interface {
	Send(*notify.Notification) (uint32, error)
	Server() notify.Server
}

type trayIcon interface {
	Set(tray.State)
}

type mailKey struct {
	account string
	uid     uint32
}

// openRequest is a click on the tray icon (uid 0) or a notification.
type openRequest struct {
	account string
	uid     uint32
}

type accountState struct {
	cfg     *config.Account
	own     []string // lowercased own addresses
	inbox   int64    // cache folder id of INBOX
	unread  int
	online  bool
	err     error
	pending []imapsync.NewMail // waiting for the burst to end

	summaryID    uint32 // open summary notification, 0 if none
	summaryCount int
}

// hub turns sync events into notifications and tray state. All of its state
// is owned by the run goroutine; others talk to it through channels.
type hub struct {
	log      *slog.Logger
	db       *cache.DB
	notifier notifier
	tray     trayIcon
	signals  emitter // nil until the D-Bus service is up
	delay    time.Duration

	order    []string
	accounts map[string]*accountState
	newMail  map[mailKey]bool // new since the user last looked

	events  chan imapsync.Event
	opens   chan openRequest
	closed  chan uint32 // a summary notification was closed
	flushAt <-chan time.Time

	statusReq  chan chan []rpc.AccountStatus
	lastStatus []rpc.AccountStatus
}

func newHub(ctx context.Context, cfg *config.Config, db *cache.DB, n notifier, t trayIcon, log *slog.Logger) (*hub, error) {
	h := &hub{
		log: log, db: db, notifier: n, tray: t, delay: burstDelay,
		accounts:  map[string]*accountState{},
		newMail:   map[mailKey]bool{},
		events:    make(chan imapsync.Event, 256),
		opens:     make(chan openRequest, 8),
		closed:    make(chan uint32, 8),
		statusReq: make(chan chan []rpc.AccountStatus),
	}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		id, err := db.EnsureAccount(ctx, a.Name)
		if err != nil {
			return nil, err
		}
		inbox, err := db.EnsureFolder(ctx, id, "INBOX")
		if err != nil {
			return nil, err
		}
		h.order = append(h.order, a.Name)
		h.accounts[a.Name] = &accountState{cfg: a, own: a.Addresses(), inbox: inbox.ID}
	}
	return h, nil
}

// notify is the imapsync.Options.Notify callback for every account.
func (h *hub) notify(ev imapsync.Event) { h.events <- ev }

// activate is the tray icon's click handler.
func (h *hub) activate() {
	select {
	case h.opens <- openRequest{}:
	default:
	}
}

// emitter sends the daemon's D-Bus signals; see service.
type emitter interface {
	FolderChanged(folderID int64)
	StatusChanged()
}

// status returns every account's state, for the D-Bus Status method.
func (h *hub) status(ctx context.Context) ([]rpc.AccountStatus, error) {
	reply := make(chan []rpc.AccountStatus, 1)
	select {
	case h.statusReq <- reply:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case st := <-reply:
		return st, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *hub) snapshot() []rpc.AccountStatus {
	out := make([]rpc.AccountStatus, 0, len(h.order))
	for _, name := range h.order {
		a := h.accounts[name]
		st := rpc.AccountStatus{Name: name, Online: a.online, Unread: int32(a.unread)}
		if a.err != nil {
			st.Error = a.err.Error()
		}
		for k := range h.newMail {
			if k.account == name {
				st.New++
			}
		}
		out = append(out, st)
	}
	return out
}

// statusChanged emits StatusChanged if anything Status reports changed.
func (h *hub) statusChanged() {
	st := h.snapshot()
	if slices.Equal(st, h.lastStatus) {
		return
	}
	h.lastStatus = st
	if h.signals != nil {
		h.signals.StatusChanged()
	}
}

func (h *hub) run(ctx context.Context) {
	h.updateTray()
	h.lastStatus = h.snapshot()
	for {
		select {
		case reply := <-h.statusReq:
			reply <- h.snapshot()
			continue
		case ev := <-h.events:
			if fc, ok := ev.(imapsync.FolderChanged); ok {
				if h.signals != nil {
					h.signals.FolderChanged(fc.FolderID)
				}
				continue
			}
			h.handle(ctx, ev)
		case <-h.flushAt:
			h.flushAt = nil
			h.flush()
		case req := <-h.opens:
			h.open(req)
		case id := <-h.closed:
			for _, a := range h.accounts {
				if a.summaryID == id {
					a.summaryID, a.summaryCount = 0, 0
				}
			}
			continue
		case <-ctx.Done():
			return
		}
		h.updateTray()
		h.statusChanged()
	}
}

func (h *hub) handle(ctx context.Context, ev imapsync.Event) {
	switch ev := ev.(type) {
	case imapsync.NewMail:
		a := h.accounts[ev.Account]
		notifyIt, reason := shouldNotify(a.own, ev)
		h.log.Info("new mail", "account", ev.Account, "from", ev.Envelope.From.String(),
			"subject", ev.Envelope.Subject, "notify", notifyIt, "reason", reason)
		if !notifyIt {
			return
		}
		h.newMail[mailKey{ev.Account, ev.UID}] = true
		a.pending = append(a.pending, ev)
		if h.flushAt == nil {
			h.flushAt = time.After(h.delay)
		}
	case imapsync.UnreadChanged:
		a := h.accounts[ev.Account]
		a.unread = ev.Unread
		h.pruneRead(ctx, ev.Account, a)
	case imapsync.ConnectionChanged:
		a := h.accounts[ev.Account]
		a.online, a.err = ev.Online, ev.Err
		if ev.Online {
			h.log.Info("online", "account", ev.Account)
		} else {
			h.log.Warn("offline", "account", ev.Account, "err", ev.Err)
		}
	}
}

// pruneRead forgets "new" mail that has been read elsewhere or deleted.
func (h *hub) pruneRead(ctx context.Context, account string, a *accountState) {
	has := false
	for k := range h.newMail {
		if k.account == account {
			has = true
			break
		}
	}
	if !has {
		return
	}
	flags, err := h.db.Flags(ctx, a.inbox)
	if err != nil {
		h.log.Warn("reading flags", "err", err)
		return
	}
	for k := range h.newMail {
		if k.account != account {
			continue
		}
		f, exists := flags[k.uid]
		if !exists || slices.Contains(f, `\Seen`) {
			delete(h.newMail, k)
		}
	}
}

// open handles a click: the user is looking now, so nothing is new.
func (h *hub) open(req openRequest) {
	if req.uid != 0 {
		h.log.Info("open message requested (the GUI doesn't exist yet)", "account", req.account, "uid", req.uid)
	} else {
		h.log.Info("tray clicked (the GUI doesn't exist yet)")
	}
	clear(h.newMail)
}

// flush shows notifications for the mail gathered during a burst.
func (h *hub) flush() {
	for _, name := range h.order {
		a := h.accounts[name]
		mails := a.pending
		a.pending = nil
		if len(mails) == 0 || h.notifier == nil {
			continue
		}
		if a.summaryID != 0 || len(mails) >= burstThreshold || !*a.cfg.NotifyDetails {
			h.sendSummary(a, mails)
			continue
		}
		for _, m := range mails {
			h.send(&notify.Notification{
				Summary: displayName(m.Envelope.From),
				Body:    subjectOrNone(m.Envelope.Subject),
			}, openRequest{m.Account, m.UID})
		}
	}
}

// sendSummary shows one notification for several messages, updating the
// account's open summary in place if there is one.
func (h *hub) sendSummary(a *accountState, mails []imapsync.NewMail) {
	count := a.summaryCount + len(mails)
	n := &notify.Notification{ReplacesID: a.summaryID}
	switch {
	case count == 1:
		n.Summary = "New mail"
	default:
		n.Summary = fmt.Sprintf("%d new messages", count)
	}
	if len(h.order) > 1 {
		n.Summary += " for " + a.cfg.Name
	}
	if *a.cfg.NotifyDetails {
		var lines []string
		for i, m := range mails {
			if i == summaryLines {
				lines = append(lines, fmt.Sprintf("and %d more", len(mails)-summaryLines))
				break
			}
			lines = append(lines, displayName(m.Envelope.From)+": "+subjectOrNone(m.Envelope.Subject))
		}
		n.Body = strings.Join(lines, "\n")
	}
	// When the summary goes away, the next burst starts a fresh count.
	n.OnClosed = func(id, _ uint32) {
		select {
		case h.closed <- id:
		default:
		}
	}
	id := h.send(n, openRequest{account: a.cfg.Name})
	if id != 0 {
		a.summaryID, a.summaryCount = id, count
	}
}

// send fills in what every foonmail notification has and shows it.
func (h *hub) send(n *notify.Notification, open openRequest) uint32 {
	n.AppName = "foonmail"
	n.Icon = "mail-unread"
	n.Category = "email.arrived"
	n.Urgency = 1
	if h.notifier.Server().Has("actions") {
		n.Actions = []notify.Action{{Key: "default", Label: "Open"}}
		n.OnAction = func(key string) {
			if key == "default" {
				select {
				case h.opens <- open:
				default:
				}
			}
		}
	}
	id, err := h.notifier.Send(n)
	if err != nil {
		h.log.Warn("notification failed", "err", err)
		return 0
	}
	return id
}

// updateTray shows new mail, unread counts and connection problems.
func (h *hub) updateTray() {
	if h.tray == nil {
		return
	}
	s := tray.State{Status: tray.Active, IconName: "mail-read"}
	lines := []string{"foonmail"}
	if n := len(h.newMail); n > 0 {
		s.Status, s.IconName = tray.NeedsAttention, "mail-unread"
		if n == 1 {
			lines = append(lines, "1 new message")
		} else {
			lines = append(lines, fmt.Sprintf("%d new messages", n))
		}
	}
	for _, name := range h.order {
		a := h.accounts[name]
		switch {
		case a.online:
			lines = append(lines, fmt.Sprintf("%s: %d unread", name, a.unread))
		case a.err != nil:
			lines = append(lines, fmt.Sprintf("%s: offline (%v)", name, a.err))
		default:
			lines = append(lines, name+": connecting…")
		}
	}
	s.Tooltip = strings.Join(lines, "\n")
	h.tray.Set(s)
}

// shouldNotify applies the design's rules for new-mail notifications:
// skip mail already read elsewhere (such as on the phone) and mail from the
// user's own addresses. own must be lowercased.
func shouldNotify(own []string, ev imapsync.NewMail) (bool, string) {
	if ev.Seen {
		return false, "already read"
	}
	if slices.Contains(own, strings.ToLower(ev.Envelope.From.Addr)) {
		return false, "from you"
	}
	return true, ""
}

func displayName(a cache.Address) string {
	if a.Name != "" {
		return a.Name
	}
	if a.Addr != "" {
		return a.Addr
	}
	return "Unknown sender"
}

func subjectOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no subject)"
	}
	return s
}
