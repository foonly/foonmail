package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestExpungeShiftsSequenceNumbers(t *testing.T) {
	var s mailboxState
	for _, uid := range []imap.UID{10, 11, 12, 13} {
		s.add(uid, nil)
	}
	// Two EXPUNGE 2 in a row remove seq 2 (uid 11), then the new seq 2 (uid 12).
	for _, want := range []imap.UID{11, 12} {
		if uid, ok := s.expunge(2); !ok || uid != want {
			t.Fatalf("expunge(2) = %d, %v; want %d", uid, ok, want)
		}
	}
	if !slices.Equal(s.uids, []imap.UID{10, 13}) {
		t.Errorf("uids = %v", s.uids)
	}
	if uid, _ := s.uidForSeq(2); uid != 13 {
		t.Errorf("seq 2 = uid %d, want 13", uid)
	}
	if _, ok := s.expunge(5); ok {
		t.Error("expunge past the end should fail")
	}
}

func TestAddIgnoresOldUIDs(t *testing.T) {
	var s mailboxState
	s.add(5, nil)
	if s.add(5, nil) || s.add(3, nil) {
		t.Error("add accepted a UID that is not past the last one")
	}
}

func TestSetFlagsDetectsChanges(t *testing.T) {
	var s mailboxState
	s.add(1, []imap.Flag{imap.FlagSeen})
	if _, changed := s.setFlags(1, []imap.Flag{imap.FlagSeen}); changed {
		t.Error("same flags reported as changed")
	}
	old, changed := s.setFlags(1, []imap.Flag{imap.FlagFlagged, imap.FlagSeen, imap.FlagSeen})
	if !changed || flagString(old) != `\Seen` {
		t.Errorf("changed = %v, old = %v", changed, old)
	}
	if got := flagString(s.flags[1]); got != `\Flagged \Seen` {
		t.Errorf("flags = %s", got)
	}
}

func TestDiffUIDs(t *testing.T) {
	added, removed := diffUIDs([]imap.UID{1, 2, 4, 7}, []imap.UID{2, 3, 4, 8, 9})
	if !slices.Equal(added, []imap.UID{3, 8, 9}) || !slices.Equal(removed, []imap.UID{1, 7}) {
		t.Errorf("added %v, removed %v", added, removed)
	}
}

func TestRedactWriter(t *testing.T) {
	var out bytes.Buffer
	w := newRedactWriter(&out)
	// Written in pieces, as the client does.
	for _, chunk := range []string{
		"T1 LOG", "IN kimstr \"hunter2\"\r\n",
		"T1 OK Logged in\r\n",
		"T2 LOGIN {6}\r\n", "kimstr {7}\r\n", "hunter2\r\n",
		"T3 SELECT INBOX\r\n",
		"T4 AUTHENTICATE PLAIN AGJvYgBodW50ZXIy\r\n",
	} {
		w.Write([]byte(chunk))
	}
	got := out.String()
	for _, secret := range []string{"hunter2", "kimstr", "AGJvYgBodW50ZXIy"} {
		if strings.Contains(got, secret) {
			t.Errorf("log contains %q:\n%s", secret, got)
		}
	}
	for _, keep := range []string{"T1 OK Logged in", "T3 SELECT INBOX", "T2 LOGIN <redacted>"} {
		if !strings.Contains(got, keep) {
			t.Errorf("log lost %q:\n%s", keep, got)
		}
	}
}
