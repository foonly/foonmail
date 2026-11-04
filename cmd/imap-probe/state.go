package main

import (
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// mailboxState is the probe's in-memory copy of one mailbox: enough to map
// the sequence numbers in EXPUNGE and unsolicited FETCH responses to UIDs,
// and to tell what changed while disconnected.
type mailboxState struct {
	uidValidity uint32
	modSeq      uint64     // HIGHESTMODSEQ we are in sync with (CONDSTORE)
	uids        []imap.UID // uids[seq-1]; ascending, as IMAP guarantees
	flags       map[imap.UID][]imap.Flag
}

func (s *mailboxState) reset() {
	*s = mailboxState{}
}

func (s *mailboxState) known() bool { return s.uidValidity != 0 }

func (s *mailboxState) lastUID() imap.UID {
	if len(s.uids) == 0 {
		return 0
	}
	return s.uids[len(s.uids)-1]
}

func (s *mailboxState) uidForSeq(seq uint32) (imap.UID, bool) {
	if seq == 0 || int(seq) > len(s.uids) {
		return 0, false
	}
	return s.uids[seq-1], true
}

// expunge removes the message at seq, shifting later sequence numbers down
// as the server does.
func (s *mailboxState) expunge(seq uint32) (imap.UID, bool) {
	uid, ok := s.uidForSeq(seq)
	if !ok {
		return 0, false
	}
	s.uids = slices.Delete(s.uids, int(seq-1), int(seq))
	delete(s.flags, uid)
	return uid, true
}

// add appends a message that arrived after the last known one.
func (s *mailboxState) add(uid imap.UID, flags []imap.Flag) bool {
	if uid <= s.lastUID() {
		return false
	}
	s.uids = append(s.uids, uid)
	s.setFlags(uid, flags)
	return true
}

// setFlags stores flags for uid and reports the previous flags and whether
// they changed.
func (s *mailboxState) setFlags(uid imap.UID, flags []imap.Flag) (old []imap.Flag, changed bool) {
	if s.flags == nil {
		s.flags = map[imap.UID][]imap.Flag{}
	}
	old, existed := s.flags[uid]
	norm := normFlags(flags)
	s.flags[uid] = norm
	return old, !existed || !slices.Equal(old, norm)
}

func (s *mailboxState) unread() int {
	n := 0
	for _, uid := range s.uids {
		if !slices.Contains(s.flags[uid], imap.FlagSeen) {
			n++
		}
	}
	return n
}

// diffUIDs compares two ascending UID lists.
func diffUIDs(old, cur []imap.UID) (added, removed []imap.UID) {
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

func normFlags(flags []imap.Flag) []imap.Flag {
	out := slices.Clone(flags)
	slices.Sort(out)
	return slices.Compact(out)
}

func flagString(flags []imap.Flag) string {
	if len(flags) == 0 {
		return "(none)"
	}
	s := make([]string, len(flags))
	for i, f := range flags {
		s[i] = string(f)
	}
	return strings.Join(s, " ")
}
