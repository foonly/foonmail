package imapsync

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
)

type eventKind int

const (
	evExists eventKind = iota
	evExpunge
	evFetch
)

// serverEvent is unsolicited data, passed from go-imap's reader goroutine
// to the session loop. Handlers must not send commands themselves.
type serverEvent struct {
	kind  eventKind
	n     uint32
	fetch *imapclient.FetchMessageBuffer
}

// live is the INBOX state the IDLE loop needs: UIDs by sequence number, to
// map EXPUNGE and unsolicited FETCH to UIDs.
type live struct {
	folder cache.Folder
	uids   []uint32
	modseq uint64
}

func (l *live) lastUID() uint32 {
	if len(l.uids) == 0 {
		return 0
	}
	return l.uids[len(l.uids)-1]
}

func (s *Syncer) inboxSession(ctx context.Context, wake <-chan struct{}) error {
	events := make(chan serverEvent, 1024)
	c, err := s.connect(&imapclient.UnilateralDataHandler{
		Expunge: func(seq uint32) { events <- serverEvent{kind: evExpunge, n: seq} },
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				events <- serverEvent{kind: evExists, n: *d.NumMessages}
			}
		},
		Fetch: func(m *imapclient.FetchMessageData) {
			if buf, err := m.Collect(); err == nil {
				events <- serverEvent{kind: evFetch, fetch: buf}
			}
		},
	})
	if err != nil {
		return err
	}
	defer c.Close()

	condstore := c.Caps().Has(imap.CapCondStore)
	folder, err := s.db.EnsureFolder(ctx, s.accountID, "INBOX")
	if err != nil {
		return err
	}
	sel, err := selectFolder(c, "INBOX", false, condstore)
	if err != nil {
		return err
	}
	s.setBaseline(sel)
	start := time.Now()
	res, err := s.syncSelected(ctx, c, folder, sel, condstore)
	if err != nil {
		return err
	}
	s.log.Info("INBOX synced", "messages", len(res.uids), "new", len(res.added), "gone", len(res.removed),
		"flag_changes", res.flagChanges, "ms", since(start), "method", res.method)
	s.notifyNew(res.added)
	s.notify(ConnectionChanged{Account: s.acct.Name, Online: true})
	s.notifyUnread(ctx, folder.ID)

	l := &live{folder: folder, uids: res.uids, modseq: sel.HighestModSeq}
	l.folder.UIDValidity = sel.UIDValidity
	return s.idleLoop(ctx, c, wake, events, l, condstore)
}

func (s *Syncer) idleLoop(ctx context.Context, c *imapclient.Client, wake <-chan struct{},
	events chan serverEvent, l *live, condstore bool) error {
	check := time.NewTicker(s.opts.CheckInterval)
	defer check.Stop()
	for {
		var idle *imapclient.IdleCommand
		if err := do(c, "idle", func() (err error) {
			idle, err = c.Idle()
			return err
		}); err != nil {
			return err
		}
		stopIdle := func() error {
			return do(c, "stop idle", func() error {
				if err := idle.Close(); err != nil {
					return err
				}
				return idle.Wait()
			})
		}

		var batch []serverEvent
	wait:
		for {
			select {
			case ev := <-events:
				batch = append(batch, ev)
				break wait
			case <-check.C:
				break wait
			case <-wake:
				return errResumed
			case <-c.Closed():
				return errors.New("connection closed by server")
			case <-ctx.Done():
				_ = stopIdle()
				_ = do(c, "logout", func() error { return c.Logout().Wait() })
				return ctx.Err()
			}
		}

		if err := stopIdle(); err != nil {
			return err
		}
		// Out of IDLE, send NOOP until it brings nothing new. A server holds
		// back updates that happen between commands, and RFC 3501 forbids
		// EXPUNGE during FETCH, so expunges made while fetchNew ran would
		// otherwise wait until some later command. NOOP also serves as the
		// periodic liveness check.
		for {
			if err := do(c, "noop", func() error { return c.Noop().Wait() }); err != nil {
				return err
			}
			for len(events) > 0 {
				batch = append(batch, <-events)
			}
			if len(batch) == 0 {
				break
			}
			if err := s.applyEvents(ctx, c, l, batch, condstore); err != nil {
				return err
			}
			batch = nil
		}
	}
}

// applyEvents writes a batch of unsolicited updates to the cache.
func (s *Syncer) applyEvents(ctx context.Context, c *imapclient.Client, l *live, batch []serverEvent, condstore bool) error {
	var flagUpdates []cache.FlagUpdate
	var removed []uint32
	exists := -1
	flush := func() error {
		if err := s.db.SetFlags(ctx, l.folder.ID, flagUpdates); err != nil {
			return err
		}
		flagUpdates = nil
		return nil
	}

	s.log.Debug("applying server updates", "count", len(batch))
	for _, ev := range batch {
		s.log.Debug("server update", "kind", ev.kind, "n", ev.n, "known", len(l.uids))
		switch ev.kind {
		case evExists:
			exists = int(ev.n)
		case evExpunge:
			if ev.n == 0 || int(ev.n) > len(l.uids) {
				s.log.Warn("EXPUNGE for unknown sequence number", "seq", ev.n)
				continue
			}
			uid := l.uids[ev.n-1]
			l.uids = slices.Delete(l.uids, int(ev.n-1), int(ev.n))
			removed = append(removed, uid)
		case evFetch:
			f := ev.fetch
			uid := uint32(f.UID)
			if uid == 0 {
				if f.SeqNum == 0 || int(f.SeqNum) > len(l.uids) {
					s.log.Warn("FETCH for unknown sequence number", "seq", f.SeqNum)
					continue
				}
				uid = l.uids[f.SeqNum-1]
			}
			if f.Flags != nil {
				flagUpdates = append(flagUpdates, cache.FlagUpdate{UID: uid, Flags: flagStrings(f.Flags), ModSeq: f.ModSeq})
			}
			l.modseq = max(l.modseq, f.ModSeq)
		}
	}

	// Flags before removals, so an update for a message expunged later in
	// the same batch is simply ignored by the cache.
	if err := flush(); err != nil {
		return err
	}
	if err := s.db.RemoveUIDs(ctx, l.folder.ID, removed); err != nil {
		return err
	}
	for _, uid := range removed {
		s.log.Debug("expunged", "uid", uid)
	}

	if exists > len(l.uids) {
		if err := s.fetchNew(ctx, c, l, condstore); err != nil {
			return err
		}
	} else if exists >= 0 && exists < len(l.uids) {
		s.log.Warn("EXISTS below the known message count", "exists", exists, "known", len(l.uids))
	}

	if err := s.db.SaveSyncState(ctx, l.folder.ID, l.folder.UIDValidity, l.modseq, l.lastUID()+1); err != nil {
		return err
	}
	s.notifyUnread(ctx, l.folder.ID)
	return nil
}

// fetchNew fetches messages after the last known UID.
func (s *Syncer) fetchNew(ctx context.Context, c *imapclient.Client, l *live, condstore bool) error {
	msgs, err := s.fetchMessages(c, imap.UIDSet{{Start: imap.UID(l.lastUID() + 1), Stop: 0}}, condstore)
	if err != nil {
		return err
	}
	// "n:*" returns the last message even when there is nothing past n.
	msgs = slices.DeleteFunc(msgs, func(m cache.NewMessage) bool { return m.UID <= l.lastUID() })
	slices.SortFunc(msgs, func(a, b cache.NewMessage) int { return int(a.UID) - int(b.UID) })
	if len(msgs) == 0 {
		return nil
	}
	if err := s.db.AddMessages(ctx, l.folder.ID, msgs); err != nil {
		return err
	}
	for _, m := range msgs {
		l.uids = append(l.uids, m.UID)
		l.modseq = max(l.modseq, m.ModSeq)
	}
	s.log.Info("new mail", "count", len(msgs))
	s.notifyNew(msgs)
	return nil
}

// setBaseline records INBOX's UIDNEXT the first time it is selected in
// this process (and again if UIDVALIDITY changes). Only messages at or above
// it count as newly arrived: mail already there when the program started
// doesn't notify, however far the first sync got before a disconnect.
func (s *Syncer) setBaseline(sel *imap.SelectData) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baselineValidity != sel.UIDValidity {
		s.baselineValidity = sel.UIDValidity
		s.baselineUID = uint32(sel.UIDNext)
	}
}

// notifyNew reports messages that arrived after the baseline.
func (s *Syncer) notifyNew(msgs []cache.NewMessage) {
	s.mu.Lock()
	baseline := s.baselineUID
	s.mu.Unlock()
	for _, m := range msgs {
		if m.UID < baseline {
			continue
		}
		s.notify(NewMail{
			Account:  s.acct.Name,
			UID:      m.UID,
			Envelope: m.Envelope,
			Seen:     slices.Contains(m.Flags, string(imap.FlagSeen)),
		})
	}
}

func (s *Syncer) notifyUnread(ctx context.Context, folderID int64) {
	if s.opts.Notify == nil {
		return
	}
	n, err := s.db.UnreadCount(ctx, folderID)
	if err != nil {
		s.log.Warn("unread count", "err", err)
		return
	}
	s.notify(UnreadChanged{Account: s.acct.Name, Unread: n})
}
