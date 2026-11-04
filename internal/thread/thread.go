// Package thread groups messages into conversations.
//
// Messages are linked through their Message-ID, In-Reply-To and References
// headers: every message is joined with each message it refers to. Two
// replies to the same message end up together even when that message is
// missing (deleted, or never received). Subjects are not used: grouping
// by subject merges unrelated mail like "Invoice" or "Question".
package thread

import (
	"sort"
	"strconv"
)

// Message is what grouping needs to know about a message.
type Message struct {
	MessageID  string   // without <>; may be empty
	InReplyTo  string   // without <>
	References []string // without <>
}

// Group returns conversations as lists of indices into msgs. Within a
// conversation, indices keep the order of msgs; conversations are ordered
// by their first index.
func Group(msgs []Message) [][]int {
	uf := unionFind{parent: map[string]string{}}
	keys := make([]string, len(msgs))
	for i, m := range msgs {
		keys[i] = key(i, m)
		uf.find(keys[i])
	}
	for i, m := range msgs {
		for _, ref := range m.References {
			if ref != "" {
				uf.union(keys[i], "id:"+ref)
			}
		}
		if m.InReplyTo != "" {
			uf.union(keys[i], "id:"+m.InReplyTo)
		}
	}

	groups := map[string][]int{}
	var roots []string
	for i := range msgs {
		r := uf.find(keys[i])
		if _, ok := groups[r]; !ok {
			roots = append(roots, r)
		}
		groups[r] = append(groups[r], i)
	}
	out := make([][]int, len(roots))
	for i, r := range roots {
		out[i] = groups[r]
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a][0] < out[b][0] })
	return out
}

// key identifies a message: by Message-ID, or by position when it has none
// (such a message can still be referred to by nothing, so it stays alone
// unless it refers to others).
func key(i int, m Message) string {
	if m.MessageID != "" {
		return "id:" + m.MessageID
	}
	return "#" + itoa(i)
}

func itoa(i int) string { return strconv.Itoa(i) }

type unionFind struct {
	parent map[string]string
}

func (u *unionFind) find(x string) string {
	p, ok := u.parent[x]
	if !ok {
		u.parent[x] = x
		return x
	}
	if p == x {
		return x
	}
	root := u.find(p)
	u.parent[x] = root
	return root
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[rb] = ra
	}
}
