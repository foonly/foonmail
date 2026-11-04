package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const commandTimeout = 30 * time.Second

var errResumed = errors.New("system resumed from sleep")

type prober struct {
	addr, user, pass, mailbox string
	check                     time.Duration // how often to verify the connection is alive
	debug                     io.Writer
	dial                      func(string, *imapclient.Options) (*imapclient.Client, error) // nil: implicit TLS
	out                       *log.Logger

	state       mailboxState
	printedInfo bool // capabilities and folders are printed once
}

type eventKind int

const (
	evExists eventKind = iota
	evExpunge
	evFetch
)

// event is unsolicited server data, passed from go-imap's reader goroutine
// to the session loop. Handlers must not send commands themselves.
type event struct {
	kind  eventKind
	n     uint32
	fetch *imapclient.FetchMessageBuffer
}

// session runs one connection until it fails, the system resumes from
// sleep, or ctx is cancelled.
func (p *prober) session(ctx context.Context, wake <-chan struct{}) error {
	events := make(chan event, 1024)
	opts := &imapclient.Options{
		Dialer:      &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		DebugWriter: p.debug,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Expunge: func(seq uint32) { events <- event{kind: evExpunge, n: seq} },
			Mailbox: func(d *imapclient.UnilateralDataMailbox) {
				if d.NumMessages != nil {
					events <- event{kind: evExists, n: *d.NumMessages}
				}
			},
			Fetch: func(m *imapclient.FetchMessageData) {
				if buf, err := m.Collect(); err == nil {
					events <- event{kind: evFetch, fetch: buf}
				}
			},
		},
	}

	p.out.Printf("connecting to %s", p.addr)
	dial := p.dial
	if dial == nil {
		dial = imapclient.DialTLS
	}
	c, err := dial(p.addr, opts)
	if err != nil {
		return err
	}
	defer c.Close()

	if !p.printedInfo {
		p.out.Printf("capabilities before login: %s", capList(c.Caps()))
	}
	if err := p.do(c, "login", func() error { return c.Login(p.user, p.pass).Wait() }); err != nil {
		return err
	}
	caps := c.Caps()
	if !p.printedInfo {
		p.reportCaps(caps)
		if err := p.reportFolders(c, caps); err != nil {
			return err
		}
		p.printedInfo = true
	}

	condstore := caps.Has(imap.CapCondStore)
	var sel *imap.SelectData
	if err := p.do(c, "select", func() (err error) {
		sel, err = c.Select(p.mailbox, &imap.SelectOptions{CondStore: condstore}).Wait()
		return err
	}); err != nil {
		return err
	}
	p.out.Printf("%s selected: %d messages, UIDVALIDITY %d, UIDNEXT %d, HIGHESTMODSEQ %d",
		p.mailbox, sel.NumMessages, sel.UIDValidity, sel.UIDNext, sel.HighestModSeq)
	if err := p.resync(c, sel, condstore); err != nil {
		return err
	}

	check := time.NewTicker(p.check)
	defer check.Stop()
	p.out.Printf("IDLE on %s; liveness check every %s. Ctrl+C to stop.", p.mailbox, p.check)
	for {
		var idle *imapclient.IdleCommand
		if err := p.do(c, "idle", func() (err error) {
			idle, err = c.Idle()
			return err
		}); err != nil {
			return err
		}
		stopIdle := func() error {
			return p.do(c, "stop idle", func() error {
				if err := idle.Close(); err != nil {
					return err
				}
				return idle.Wait()
			})
		}

		var fetchNew, checkAlive bool
	wait:
		for {
			select {
			case ev := <-events:
				if p.apply(ev) {
					fetchNew = true
					break wait
				}
			case <-check.C:
				checkAlive = true
				break wait
			case <-wake:
				return errResumed
			case <-c.Closed():
				return errors.New("connection closed by server")
			case <-ctx.Done():
				_ = stopIdle()
				_ = p.do(c, "logout", func() error { return c.Logout().Wait() })
				return ctx.Err()
			}
		}

		if err := stopIdle(); err != nil {
			return err
		}
		if checkAlive {
			start := time.Now()
			if err := p.do(c, "noop", func() error { return c.Noop().Wait() }); err != nil {
				return err
			}
			p.out.Printf("connection alive (NOOP %d ms)", time.Since(start).Milliseconds())
		}
		// Events may have arrived while IDLE was stopping or during NOOP.
		for len(events) > 0 {
			if p.apply(<-events) {
				fetchNew = true
			}
		}
		if fetchNew {
			if err := p.fetchNew(c, condstore); err != nil {
				return err
			}
		}
	}
}

// do runs an IMAP command and gives up after commandTimeout, closing the
// connection so the blocked command returns.
func (p *prober) do(c *imapclient.Client, what string, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	case <-time.After(commandTimeout):
		c.Close()
		return fmt.Errorf("%s: no response in %s", what, commandTimeout)
	}
}

// apply updates state from unsolicited data and prints what changed. It
// reports whether new messages need to be fetched.
func (p *prober) apply(ev event) bool {
	s := &p.state
	switch ev.kind {
	case evExists:
		if int(ev.n) > len(s.uids) {
			return true
		}
		if int(ev.n) < len(s.uids) {
			p.out.Printf("WARNING: EXISTS %d is below the %d messages we know of", ev.n, len(s.uids))
		}
	case evExpunge:
		if uid, ok := s.expunge(ev.n); ok {
			p.out.Printf("GONE   uid %d (expunged)", uid)
		} else {
			p.out.Printf("WARNING: EXPUNGE for unknown sequence number %d", ev.n)
		}
	case evFetch:
		f := ev.fetch
		uid := f.UID
		if uid == 0 {
			var ok bool
			if uid, ok = s.uidForSeq(f.SeqNum); !ok {
				p.out.Printf("WARNING: FETCH for unknown sequence number %d", f.SeqNum)
				return false
			}
		}
		if f.Flags != nil {
			if old, changed := s.setFlags(uid, f.Flags); changed {
				p.out.Printf("FLAGS  uid %d: %s -> %s", uid, flagString(old), flagString(s.flags[uid]))
			}
		}
		if f.ModSeq > s.modSeq {
			s.modSeq = f.ModSeq
		}
	}
	return false
}

// fetchNew fetches messages beyond the last known sequence number.
func (p *prober) fetchNew(c *imapclient.Client, condstore bool) error {
	from := uint32(len(p.state.uids)) + 1
	var msgs []*imapclient.FetchMessageBuffer
	err := p.do(c, "fetch new", func() (err error) {
		msgs, err = c.Fetch(imap.SeqSet{{Start: from, Stop: 0}},
			&imap.FetchOptions{UID: true, Flags: true, Envelope: true, ModSeq: condstore}).Collect()
		return err
	})
	if err != nil {
		return err
	}
	for _, m := range msgs {
		// "n:*" returns the last message even when n is past the end.
		if !p.state.add(m.UID, m.Flags) {
			continue
		}
		p.out.Printf("NEW    uid %d %s", m.UID, describe(m))
		if m.ModSeq > p.state.modSeq {
			p.state.modSeq = m.ModSeq
		}
	}
	return nil
}

// resync brings the state up to date after (re)connecting. go-imap has no
// QRESYNC support, so this is the CONDSTORE fallback: the full UID list to
// find expunges, plus FLAGS CHANGEDSINCE for flag changes.
func (p *prober) resync(c *imapclient.Client, sel *imap.SelectData, condstore bool) error {
	s := &p.state
	start := time.Now()
	if s.known() && s.uidValidity != sel.UIDValidity {
		p.out.Printf("UIDVALIDITY changed (%d -> %d): dropping everything known about %s",
			s.uidValidity, sel.UIDValidity, p.mailbox)
		s.reset()
	}
	first := !s.known()

	if sel.NumMessages == 0 {
		for _, uid := range s.uids {
			p.out.Printf("GONE   uid %d (while disconnected)", uid)
		}
		s.uids, s.flags = nil, nil
		s.uidValidity, s.modSeq = sel.UIDValidity, sel.HighestModSeq
		p.out.Printf("%s is empty", p.mailbox)
		return nil
	}

	// Cheap path: UIDs only, flags only where they changed.
	cheap := !first && condstore && s.modSeq > 0
	var all []*imapclient.FetchMessageBuffer
	err := p.do(c, "fetch uids", func() (err error) {
		all, err = c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}},
			&imap.FetchOptions{UID: true, Flags: !cheap}).Collect()
		return err
	})
	if err != nil {
		return err
	}
	cur := make([]imap.UID, len(all))
	for i, m := range all {
		cur[i] = m.UID
	}
	added, removed := diffUIDs(s.uids, cur)

	oldFlags := s.flags
	s.uids, s.flags = cur, map[imap.UID][]imap.Flag{}
	for _, uid := range cur {
		if f, ok := oldFlags[uid]; ok {
			s.flags[uid] = f
		}
	}

	flagChanges := 0
	report := func(uid imap.UID, flags []imap.Flag) {
		if old, changed := s.setFlags(uid, flags); changed && !first && !slices.Contains(added, uid) {
			flagChanges++
			p.out.Printf("FLAGS  uid %d: %s -> %s (while disconnected)", uid, flagString(old), flagString(s.flags[uid]))
		}
	}
	if cheap {
		var changed []*imapclient.FetchMessageBuffer
		err := p.do(c, "fetch changed flags", func() (err error) {
			changed, err = c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}},
				&imap.FetchOptions{UID: true, Flags: true, ChangedSince: s.modSeq}).Collect()
			return err
		})
		if err != nil {
			return err
		}
		for _, m := range changed {
			report(m.UID, m.Flags)
		}
	} else {
		for _, m := range all {
			report(m.UID, m.Flags)
		}
	}

	if !first {
		for _, uid := range removed {
			p.out.Printf("GONE   uid %d (while disconnected)", uid)
		}
		if len(added) > 0 {
			var msgs []*imapclient.FetchMessageBuffer
			err := p.do(c, "fetch added", func() (err error) {
				msgs, err = c.Fetch(imap.UIDSetNum(added...),
					&imap.FetchOptions{UID: true, Flags: true, Envelope: true}).Collect()
				return err
			})
			if err != nil {
				return err
			}
			for _, m := range msgs {
				s.setFlags(m.UID, m.Flags)
				p.out.Printf("NEW    uid %d %s (while disconnected)", m.UID, describe(m))
			}
		}
	}

	s.uidValidity, s.modSeq = sel.UIDValidity, sel.HighestModSeq
	method := "full UID and FLAGS fetch"
	if cheap {
		method = "UID list + FLAGS CHANGEDSINCE (CONDSTORE)"
	}
	if first {
		p.out.Printf("loaded %d messages, %d unread, in %d ms (%s)",
			len(s.uids), s.unread(), time.Since(start).Milliseconds(), method)
	} else {
		p.out.Printf("resync: %d new, %d gone, %d flag changes, in %d ms (%s)",
			len(added), len(removed), flagChanges, time.Since(start).Milliseconds(), method)
	}
	return nil
}

func describe(m *imapclient.FetchMessageBuffer) string {
	from, subject := "?", ""
	if e := m.Envelope; e != nil {
		subject = e.Subject
		if len(e.From) > 0 {
			a := e.From[0]
			from = a.Addr()
			if a.Name != "" {
				from = fmt.Sprintf("%s <%s>", a.Name, a.Addr())
			}
		}
	}
	state := "unread"
	if slices.Contains(m.Flags, imap.FlagSeen) {
		state = "read"
	}
	return fmt.Sprintf("from %s: %q [%s]", from, subject, state)
}

// Reports printed once per run.

var interestingCaps = []string{
	"IDLE", "CONDSTORE", "QRESYNC", "SPECIAL-USE", "LIST-STATUS", "ESEARCH", "MOVE",
	"UIDPLUS", "NOTIFY", "SORT", "THREAD=REFERENCES", "APPENDLIMIT", "COMPRESS=DEFLATE", "X-GM-EXT-1",
}

func (p *prober) reportCaps(caps imap.CapSet) {
	p.out.Printf("capabilities after login: %s", capList(caps))
	var have, missing []string
	for _, want := range interestingCaps {
		found := false
		for c := range caps {
			if string(c) == want || strings.HasPrefix(string(c), want+"=") {
				found = true
				break
			}
		}
		if found {
			have = append(have, want)
		} else {
			missing = append(missing, want)
		}
	}
	p.out.Printf("  supported: %s", strings.Join(have, ", "))
	p.out.Printf("  missing:   %s", strings.Join(missing, ", "))
	if caps.Has(imap.CapQResync) {
		p.out.Printf("  note: the server has QRESYNC, but go-imap v2 can't use it; resync uses CONDSTORE")
	}
}

var specialUse = []imap.MailboxAttr{
	imap.MailboxAttrSent, imap.MailboxAttrDrafts, imap.MailboxAttrTrash, imap.MailboxAttrJunk,
	imap.MailboxAttrArchive, imap.MailboxAttrAll, imap.MailboxAttrFlagged,
}

func (p *prober) reportFolders(c *imapclient.Client, caps imap.CapSet) error {
	opts := &imap.ListOptions{ReturnSubscribed: true, ReturnSpecialUse: caps.Has(imap.CapSpecialUse)}
	if caps.Has(imap.CapListStatus) {
		opts.ReturnStatus = &imap.StatusOptions{NumMessages: true, NumUnseen: true}
	}
	var boxes []*imap.ListData
	if err := p.do(c, "list", func() (err error) {
		boxes, err = c.List("", "*", opts).Collect()
		return err
	}); err != nil {
		return err
	}
	p.out.Printf("folders (%d):", len(boxes))
	found := map[imap.MailboxAttr][]string{}
	for _, b := range boxes {
		var notes []string
		for _, a := range b.Attrs {
			if slices.Contains(specialUse, a) {
				notes = append(notes, string(a))
				found[a] = append(found[a], b.Mailbox)
			}
		}
		if slices.Contains(b.Attrs, imap.MailboxAttrSubscribed) {
			notes = append(notes, "subscribed")
		}
		if slices.Contains(b.Attrs, imap.MailboxAttrNoSelect) {
			notes = append(notes, "not selectable")
		}
		if st := b.Status; st != nil && st.NumMessages != nil && st.NumUnseen != nil {
			notes = append(notes, fmt.Sprintf("%d messages, %d unread", *st.NumMessages, *st.NumUnseen))
		}
		p.out.Printf("  %-30s %s", b.Mailbox, strings.Join(notes, ", "))
	}
	for _, a := range []imap.MailboxAttr{imap.MailboxAttrSent, imap.MailboxAttrDrafts} {
		if boxes := found[a]; len(boxes) > 0 {
			p.out.Printf("  %s folder: %s", a, strings.Join(boxes, ", "))
		} else {
			p.out.Printf("  no folder is flagged %s; Dovecot needs `special_use` set on it", a)
		}
	}
	return nil
}

func capList(caps imap.CapSet) string {
	s := make([]string, 0, len(caps))
	for c := range caps {
		s = append(s, string(c))
	}
	slices.Sort(s)
	return strings.Join(s, " ")
}
