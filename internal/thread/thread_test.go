package thread

import (
	"fmt"
	"testing"
)

func TestGroup(t *testing.T) {
	msgs := []Message{
		0: {MessageID: "a"},                                                 // root
		1: {MessageID: "b", InReplyTo: "a", References: []string{"a"}},      // reply
		2: {MessageID: "x"},                                                 // unrelated
		3: {MessageID: "c", InReplyTo: "b", References: []string{"a", "b"}}, // reply to reply
		// Two replies to a message we don't have still belong together.
		4: {MessageID: "d", InReplyTo: "gone", References: []string{"gone"}},
		5: {MessageID: "e", References: []string{"gone"}},
		// No Message-ID, but it refers to x.
		6: {InReplyTo: "x"},
		// No headers at all: alone.
		7: {},
		8: {},
		// The same subject means nothing.
		9: {MessageID: "f"},
	}
	got := fmt.Sprint(Group(msgs))
	want := "[[0 1 3] [2 6] [4 5] [7] [8] [9]]"
	if got != want {
		t.Errorf("Group = %s, want %s", got, want)
	}
}

func TestGroupWithLateParent(t *testing.T) {
	// A reply listed before the message it answers, as when sorted by folder.
	got := fmt.Sprint(Group([]Message{
		{MessageID: "r", References: []string{"p"}},
		{MessageID: "p"},
	}))
	if got != "[[0 1]]" {
		t.Errorf("Group = %s", got)
	}
}

func TestGroupLarge(t *testing.T) {
	var msgs []Message
	for i := range 3000 {
		m := Message{MessageID: fmt.Sprint(i)}
		if i%10 != 0 {
			m.InReplyTo = fmt.Sprint(i - i%10) // ten-message threads
		}
		msgs = append(msgs, m)
	}
	if n := len(Group(msgs)); n != 300 {
		t.Errorf("%d groups, want 300", n)
	}
}
