package main

import (
	"testing"
	"time"

	"foonmail/internal/cache"
)

func entry(id int64, folder int64, uid uint32, msgID, inReplyTo, from, subject string, hour int, flags ...string) cache.Entry {
	return cache.Entry{
		Summary: cache.Summary{
			ID: id, FolderID: folder, UID: uid, Subject: subject, Flags: flags,
			From: cache.Address{Name: from, Addr: map[string]string{"Bob Example": "bob@example.com",
				"Liisa Mäkelä": "liisa@example.com", "Me": "niklas@uplink.fi"}[from]},
			Date: time.Date(2026, 11, 4, hour, 0, 0, 0, time.UTC),
		},
		MessageID: msgID, InReplyTo: inReplyTo,
	}
}

func TestBuildConversations(t *testing.T) {
	const inbox, sent = 1, 2
	entries := []cache.Entry{
		entry(1, inbox, 10, "a", "", "Bob Example", "Kahvi?", 9, `\Seen`),
		entry(2, sent, 50, "b", "a", "Me", "Re: Kahvi?", 10, `\Seen`),
		entry(3, inbox, 11, "c", "b", "Liisa Mäkelä", "VS: Re: Kahvi?", 11),
		entry(4, inbox, 12, "x", "", "Bob Example", "Invoice", 8, `\Flagged`),
		entry(5, sent, 51, "s", "", "Me", "Only sent", 12, `\Seen`),
		// Mail to yourself: one message, in both folders.
		entry(6, inbox, 13, "self", "", "Me", "Note to self", 7),
		entry(6, sent, 52, "self", "", "Me", "Note to self", 7, `\Seen`),
	}
	cs := buildConversations(entries, inbox, []string{"niklas@uplink.fi"})
	if len(cs) != 3 {
		t.Fatalf("%d conversations, want 3 (the Sent-only one is left out): %+v", len(cs), cs)
	}
	k := cs[0]
	if k.subject != "Kahvi?" || len(k.members) != 3 || k.unread != 1 || k.inFolder != 2 || k.key != "a" {
		t.Errorf("thread = %+v", k)
	}
	if k.members[0].ID != 1 || k.members[1].FolderID != sent || k.members[2].ID != 3 {
		t.Errorf("members not oldest first with the Sent reply between: %+v", k.members)
	}
	if k.people != "Liisa, me, Bob" {
		t.Errorf("people = %q", k.people)
	}
	if got := k.folderUIDs(inbox, true); len(got) != 1 || got[0] != 11 {
		t.Errorf("unread UIDs = %v", got)
	}
	if !cs[1].flagged || cs[1].subject != "Invoice" {
		t.Errorf("second = %+v", cs[1])
	}
	self := cs[2]
	if len(self.members) != 1 || self.members[0].FolderID != inbox || self.unread != 1 {
		t.Errorf("note to self = %+v", self)
	}
}

func TestDeliveredCopyOfOwnMailIsOneMessage(t *testing.T) {
	const inbox, sent = 1, 2
	// The cache keeps them apart (different sizes), but they share a Message-ID.
	entries := []cache.Entry{
		entry(10, inbox, 20, "self", "", "Me", "foonmail test", 9),
		entry(11, sent, 60, "self", "", "Me", "foonmail test", 9, `\Seen`),
		// Reply-all that included me: Bob's reply, my reply-all, and its
		// copy delivered back to me.
		entry(12, inbox, 21, "q", "", "Bob Example", "Plan", 10, `\Seen`),
		entry(13, sent, 61, "r", "q", "Me", "Re: Plan", 11, `\Seen`),
		entry(14, inbox, 22, "r", "q", "Me", "Re: Plan", 11),
	}
	for _, folder := range []int64{inbox, sent} {
		cs := buildConversations(entries, folder, []string{"niklas@uplink.fi"})
		if len(cs) != 2 {
			t.Fatalf("folder %d: %d conversations, want 2", folder, len(cs))
		}
		for _, c := range cs {
			seen := map[string]bool{}
			for _, m := range c.members {
				if seen[m.MessageID] {
					t.Errorf("folder %d: %q appears twice in %q", folder, m.MessageID, c.subject)
				}
				seen[m.MessageID] = true
				if m.MessageID != "q" && m.FolderID != folder {
					t.Errorf("folder %d: showing the copy from folder %d of %q", folder, m.FolderID, m.MessageID)
				}
			}
		}
	}
	plan := buildConversations(entries, inbox, nil)[0]
	if plan.subject != "Plan" || len(plan.members) != 2 || plan.unread != 1 {
		t.Errorf("reply-all conversation in INBOX = %+v", plan)
	}
}

func TestBaseSubject(t *testing.T) {
	cases := map[string]string{
		"Re: Re: Kahvi?":       "Kahvi?",
		"VS: SV: Fwd: Kokous":  "Kokous",
		"RE[2]: Hello":         "Hello",
		"Re:":                  "Re:",
		"Reunion plans":        "Reunion plans",
		"  Fw:   Spaced  out ": "Spaced  out",
	}
	for in, want := range cases {
		if got := baseSubject(in); got != want {
			t.Errorf("baseSubject(%q) = %q, want %q", in, got, want)
		}
	}
}
