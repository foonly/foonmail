package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"foonmail/internal/cache"
	"foonmail/internal/config"
	"foonmail/internal/thread"
)

// conversation is one row of the list: the folder's messages of a thread,
// plus the user's own replies from Sent.
type conversation struct {
	key      string        // stable across reloads: the oldest member's Message-ID
	members  []cache.Entry // oldest first
	subject  string        // the oldest member's subject without Re:/Fwd:
	people   string        // senders, newest first
	latest   time.Time
	unread   int // unread members in the current folder
	flagged  bool
	inFolder int // members in the current folder

	// copies are further copies, in the current folder, of members shown
	// once: the same message delivered more than once, as to several of
	// your addresses. Reading, moving and deleting take them along, and
	// an unread copy makes its message unread.
	copies []cache.Entry
}

// folderUIDs are the UIDs of the members in folderID, optionally only
// unread ones.
func (c *conversation) folderUIDs(folderID int64, onlyUnread bool) []uint32 {
	var out []uint32
	for _, m := range slices.Concat(c.members, c.copies) {
		if m.FolderID == folderID && (!onlyUnread || unread(m.Summary)) {
			out = append(out, m.UID)
		}
	}
	return out
}

// buildConversations groups entries (a folder's messages and, for replies,
// the Sent folder's) into conversations that have at least one message in
// folderID, newest conversation first. own are the user's addresses,
// lowercased, shown as "me".
func buildConversations(entries []cache.Entry, folderID int64, own []string) []conversation {
	msgs := make([]thread.Message, len(entries))
	for i, e := range entries {
		msgs[i] = thread.Message{MessageID: e.MessageID, InReplyTo: e.InReplyTo, References: e.References}
	}
	var out []conversation
	for _, group := range thread.Group(msgs) {
		// A message in both folders is one message; show its copy in the
		// current folder. That covers mail to yourself and reply-all that
		// includes you: the delivered copy has the Sent copy's Message-ID
		// but extra headers (Received: and so on), so the cache stores two
		// messages. Matching on Message-ID only within a conversation, where
		// the threading headers already tie them together, can't merge
		// unrelated mail.
		byID := map[string]cache.Entry{}
		var copies []cache.Entry
		unreadCopy := map[string]bool{}
		for _, i := range group {
			e := entries[i]
			k := e.MessageID
			if k == "" {
				k = fmt.Sprintf("#%d", e.ID)
			}
			prev, ok := byID[k]
			switch {
			case !ok || (prev.FolderID != folderID && e.FolderID == folderID):
				byID[k] = e
			case prev.FolderID == folderID && e.FolderID == folderID:
				copies = append(copies, e)
				if unread(e.Summary) {
					unreadCopy[k] = true
				}
			}
		}
		c := conversation{copies: copies}
		for k, e := range byID {
			c.members = append(c.members, e)
			if e.FolderID == folderID {
				c.inFolder++
				if unread(e.Summary) || unreadCopy[k] {
					c.unread++
				}
				if flagged(e.Summary) {
					c.flagged = true
				}
			}
		}
		if c.inFolder == 0 {
			continue // only Sent mail: not a conversation of this folder
		}
		slices.SortFunc(c.members, func(a, b cache.Entry) int {
			if d := a.Date.Compare(b.Date); d != 0 {
				return d
			}
			return int(a.UID) - int(b.UID)
		})
		first := c.members[0]
		c.key = first.MessageID
		if c.key == "" {
			c.key = fmt.Sprintf("#%d", first.ID)
		}
		c.subject = baseSubject(first.Subject)
		c.latest = c.members[len(c.members)-1].Date
		c.people = people(c.members, own)
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b conversation) int { return b.latest.Compare(a.latest) })
	return out
}

var replyPrefix = regexp.MustCompile(`(?i)^\s*((re|fwd?|vs|sv|aw|wg|vl|tr)(\[\d+\])?\s*:\s*)+`)

// baseSubject strips reply and forward prefixes, in the languages common
// in Finnish and Nordic mail too (VS:, SV:, VL:).
func baseSubject(s string) string {
	if b := strings.TrimSpace(replyPrefix.ReplaceAllString(s, "")); b != "" {
		return b
	}
	return strings.TrimSpace(s)
}

// people lists distinct senders, newest first, at most three named.
func people(members []cache.Entry, own []string) string {
	var names []string
	seen := map[string]bool{}
	for i := len(members) - 1; i >= 0; i-- {
		from := members[i].From
		k := strings.ToLower(from.Addr)
		if seen[k] {
			continue
		}
		seen[k] = true
		// Whole names: a first word alone can be "The" or "Team". The
		// list column cuts a long line short with an ellipsis.
		name := senderName(from)
		if config.IsOwn(own, k) {
			name = "me"
		}
		names = append(names, name)
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d", len(names)-3)
	}
	return strings.Join(names, ", ")
}
