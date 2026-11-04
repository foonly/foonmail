package imapsync

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"foonmail/internal/cache"
)

// fetchChunk is how many messages one FETCH asks for when filling the cache.
const fetchChunk = 500

// referencesSection fetches just the References header, for threading.
// ENVELOPE has In-Reply-To but not References.
var referencesSection = &imap.FetchItemBodySection{
	Specifier:    imap.PartSpecifierHeader,
	HeaderFields: []string{"References"},
	Peek:         true,
}

// syncResult is what a folder sync changed.
type syncResult struct {
	uids        []uint32 // the folder's UIDs afterwards, ascending (= sequence order)
	added       []cache.NewMessage
	removed     []uint32
	flagChanges int
	method      string
}

// syncSelected brings the cache's copy of the selected folder up to date.
//
// go-imap has no QRESYNC, so this uses CONDSTORE when the server has it: the
// full UID list to find expunges, plus FLAGS CHANGEDSINCE for flag changes.
// Without CONDSTORE (or on the first sync) it fetches all flags.
func (s *Syncer) syncSelected(ctx context.Context, c *imapclient.Client, f cache.Folder,
	sel *imap.SelectData, condstore bool) (syncResult, error) {
	var res syncResult
	if f.UIDValidity != 0 && f.UIDValidity != sel.UIDValidity {
		s.log.Warn("UIDVALIDITY changed, refetching folder", "folder", f.Name,
			"old", f.UIDValidity, "new", sel.UIDValidity)
		if err := s.db.ResetFolder(ctx, f.ID, sel.UIDValidity); err != nil {
			return res, err
		}
		f.UIDValidity, f.HighestModSeq = sel.UIDValidity, 0
	}

	known, err := s.db.UIDs(ctx, f.ID)
	if err != nil {
		return res, err
	}

	cheap := f.UIDValidity == sel.UIDValidity && condstore && f.HighestModSeq > 0
	res.method = "full UID and FLAGS fetch"
	if cheap {
		res.method = "UID list + FLAGS CHANGEDSINCE"
	}

	var all []*imapclient.FetchMessageBuffer
	if sel.NumMessages > 0 {
		err = do(c, "fetch uids", func() (err error) {
			all, err = c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}},
				&imap.FetchOptions{UID: true, Flags: !cheap}).Collect()
			return err
		})
		if err != nil {
			return res, err
		}
	}
	res.uids = make([]uint32, len(all))
	for i, m := range all {
		res.uids[i] = uint32(m.UID)
	}
	addedUIDs, removed := diffUIDs(known, res.uids)
	res.removed = removed
	if err := s.db.RemoveUIDs(ctx, f.ID, removed); err != nil {
		return res, err
	}

	// Flag changes on messages we already had.
	var updates []cache.FlagUpdate
	if cheap {
		var changed []*imapclient.FetchMessageBuffer
		err = do(c, "fetch changed flags", func() (err error) {
			changed, err = c.Fetch(imap.SeqSet{{Start: 1, Stop: 0}},
				&imap.FetchOptions{UID: true, Flags: true, ModSeq: true, ChangedSince: f.HighestModSeq}).Collect()
			return err
		})
		if err != nil {
			return res, err
		}
		for _, m := range changed {
			updates = append(updates, cache.FlagUpdate{UID: uint32(m.UID), Flags: flagStrings(m.Flags), ModSeq: m.ModSeq})
		}
	} else if len(known) > 0 {
		old, err := s.db.Flags(ctx, f.ID)
		if err != nil {
			return res, err
		}
		for _, m := range all {
			prev, ok := old[uint32(m.UID)]
			if cur := flagStrings(m.Flags); ok && !slices.Equal(prev, sortedCompact(cur)) {
				updates = append(updates, cache.FlagUpdate{UID: uint32(m.UID), Flags: cur})
			}
		}
	}
	isAdded := make(map[uint32]bool, len(addedUIDs))
	for _, u := range addedUIDs {
		isAdded[u] = true
	}
	updates = slices.DeleteFunc(updates, func(u cache.FlagUpdate) bool { return isAdded[u.UID] })
	res.flagChanges = len(updates)
	if err := s.db.SetFlags(ctx, f.ID, updates); err != nil {
		return res, err
	}

	// New messages, with everything the list and threading need.
	for chunk := range slices.Chunk(addedUIDs, fetchChunk) {
		msgs, err := s.fetchMessages(c, imap.UIDSetNum(toUIDs(chunk)...), condstore)
		if err != nil {
			return res, err
		}
		if err := s.db.AddMessages(ctx, f.ID, msgs); err != nil {
			return res, err
		}
		res.added = append(res.added, msgs...)
	}

	if err := s.db.SaveSyncState(ctx, f.ID, sel.UIDValidity, sel.HighestModSeq, uint32(sel.UIDNext)); err != nil {
		return res, err
	}
	return res, nil
}

// fetchMessages fetches what the cache stores for each message in set.
func (s *Syncer) fetchMessages(c *imapclient.Client, set imap.NumSet, condstore bool) ([]cache.NewMessage, error) {
	var bufs []*imapclient.FetchMessageBuffer
	err := do(c, "fetch messages", func() (err error) {
		bufs, err = c.Fetch(set, &imap.FetchOptions{
			UID: true, Flags: true, Envelope: true, RFC822Size: true, InternalDate: true,
			ModSeq:      condstore,
			BodySection: []*imap.FetchItemBodySection{referencesSection},
		}).Collect()
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]cache.NewMessage, 0, len(bufs))
	for _, b := range bufs {
		out = append(out, toNewMessage(b))
	}
	return out, nil
}

func toNewMessage(b *imapclient.FetchMessageBuffer) cache.NewMessage {
	m := cache.NewMessage{
		UID:    uint32(b.UID),
		Flags:  flagStrings(b.Flags),
		ModSeq: b.ModSeq,
		Size:   b.RFC822Size,
	}
	if e := b.Envelope; e != nil {
		m.Envelope = cache.Envelope{
			MessageID: trimAngle(e.MessageID),
			Subject:   e.Subject,
			To:        addresses(e.To),
			Cc:        addresses(e.Cc),
			Date:      e.Date,
		}
		if from := addresses(e.From); len(from) > 0 {
			m.Envelope.From = from[0]
		}
		if len(e.InReplyTo) > 0 {
			m.Envelope.InReplyTo = trimAngle(e.InReplyTo[0])
		}
	}
	if m.Envelope.Date.IsZero() {
		m.Envelope.Date = b.InternalDate
	}
	m.Envelope.References = parseReferences(b.FindBodySection(referencesSection))
	return m
}

var msgIDPattern = regexp.MustCompile(`<([^<>\s]+)>`)

// parseReferences extracts message IDs from a raw References header.
func parseReferences(header []byte) []string {
	var ids []string
	for _, m := range msgIDPattern.FindAllSubmatch(header, -1) {
		ids = append(ids, string(m[1]))
	}
	return ids
}

func addresses(as []imap.Address) []cache.Address {
	out := make([]cache.Address, 0, len(as))
	for _, a := range as {
		if a.IsGroupStart() || a.IsGroupEnd() {
			continue
		}
		out = append(out, cache.Address{Name: a.Name, Addr: a.Addr()})
	}
	return out
}

func trimAngle(s string) string { return strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">") }

func flagStrings(flags []imap.Flag) []string {
	out := make([]string, len(flags))
	for i, f := range flags {
		out[i] = string(f)
	}
	return out
}

func sortedCompact(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return slices.Compact(s)
}

func toUIDs(us []uint32) []imap.UID {
	out := make([]imap.UID, len(us))
	for i, u := range us {
		out[i] = imap.UID(u)
	}
	return out
}

// diffUIDs compares two ascending UID lists.
func diffUIDs(old, cur []uint32) (added, removed []uint32) {
	i, j := 0, 0
	for i < len(old) || j < len(cur) {
		switch {
		case j == len(cur) || (i < len(old) && old[i] < cur[j]):
			removed = append(removed, old[i])
			i++
		case i == len(old) || cur[j] < old[i]:
			added = append(added, cur[j])
			j++
		default:
			i++
			j++
		}
	}
	return added, removed
}

func since(t time.Time) int64 { return time.Since(t).Milliseconds() }
