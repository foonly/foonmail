package main

import (
	"strings"
	"unicode"

	"foonmail/internal/config"
)

// After s (junk), the list shows the conversations in the folder that look
// like the junked ones, all marked, so the rest of a spam run can go with
// one more key: s or d act on the marks, Space unmarks one that isn't
// spam, Esc goes back to the whole list.

// similarSet is the list narrowed to look-alikes of junked mail.
type similarSet struct {
	subject string          // what they look like, for the header
	keys    map[string]bool // conversation keys

	// Where the whole list was: its keys in order and the selected row,
	// to come back to the same place, or the nearest conversation that
	// is still there.
	order []string
	sel   int
}

// returnKey is the conversation to select back in the whole list.
func (s *similarSet) returnKey(exists func(string) bool) string {
	for i := s.sel; i < len(s.order); i++ {
		if exists(s.order[i]) {
			return s.order[i]
		}
	}
	for i := min(s.sel, len(s.order)) - 1; i >= 0; i-- {
		if exists(s.order[i]) {
			return s.order[i]
		}
	}
	return ""
}

// leaveSimilar ends the narrowed list and says which conversation to
// select in the whole one.
func (u *ui) leaveSimilar() string {
	s := u.similar
	u.similar = nil
	u.clearMarks()
	have := map[string]bool{}
	for _, c := range u.convs {
		have[c.key] = true
	}
	return s.returnKey(func(k string) bool { return have[k] })
}

// sender is a conversation's sender in this folder: the newest member
// here that isn't from you.
func (u *ui) sender(c *conversation) (name, addr string, ok bool) {
	for i := len(c.members) - 1; i >= 0; i-- {
		m := c.members[i]
		if m.FolderID == u.folder.ID && !config.IsOwn(u.own, m.From.Addr) {
			return m.From.Name, strings.ToLower(m.From.Addr), true
		}
	}
	return "", "", false
}

// words splits a name or subject into lowercased words, leaving out
// numbers and punctuation, which spam varies ("Driving Glasses #1").
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r)
	})
}

// overlap is how much two word lists share: the shared distinct words
// over all distinct words (Jaccard), 0 when either is empty.
func overlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := map[string]int{}
	for _, w := range a {
		set[w] |= 1
	}
	for _, w := range b {
		set[w] |= 2
	}
	both := 0
	for _, v := range set {
		if v == 3 {
			both++
		}
	}
	return float64(both) / float64(len(set))
}

func domain(addr string) string {
	_, d, _ := strings.Cut(addr, "@")
	return d
}

// alike says whether two messages look like the same spam: from the same
// sender (by address, or by name with numbers and punctuation left out,
// as spammers change addresses), or a similar name at the same domain,
// with mostly the same subject words.
func alike(nameA, addrA, subjA, nameB, addrB, subjB string) bool {
	wa, wb := words(nameA), words(nameB)
	sameSender := addrA == addrB ||
		(len(wa) > 0 && strings.Join(wa, " ") == strings.Join(wb, " ")) ||
		(domain(addrA) == domain(addrB) && overlap(wa, wb) >= 0.5)
	return sameSender && overlap(words(subjA), words(subjB)) >= 0.5
}

// findSimilar lists the conversations in the folder, other than the given
// ones, that look like one of them. Conversations you have written in
// are left out: they aren't spam.
func (u *ui) findSimilar(like []*conversation) map[string]bool {
	type sample struct{ name, addr, subject string }
	var samples []sample
	skip := map[string]bool{}
	for _, c := range like {
		skip[c.key] = true
		if name, addr, ok := u.sender(c); ok {
			samples = append(samples, sample{name, addr, c.subject})
		}
	}
	found := map[string]bool{}
	for i := range u.convs {
		c := &u.convs[i]
		if skip[c.key] || u.wroteIn(c) {
			continue
		}
		name, addr, ok := u.sender(c)
		if !ok {
			continue
		}
		for _, s := range samples {
			if alike(s.name, s.addr, s.subject, name, addr, c.subject) {
				found[c.key] = true
				break
			}
		}
	}
	return found
}

func (u *ui) wroteIn(c *conversation) bool {
	for _, m := range c.members {
		if config.IsOwn(u.own, m.From.Addr) {
			return true
		}
	}
	return false
}

// showSimilar narrows the list to look-alikes and marks them all.
func (u *ui) showSimilar(subject string, keys map[string]bool) {
	order := make([]string, len(u.view))
	for i, ci := range u.view {
		order[i] = u.convs[ci].key
	}
	u.similar = &similarSet{subject: subject, keys: keys, order: order, sel: u.sel}
	u.clearMarks()
	for k := range keys {
		u.marks[k] = true
	}
	u.search.SetText("")
	u.searching = false
	u.mode = modeList
	u.focusReq = u.listTag
	u.applyFilter("")
}

// endSimilar goes back to the whole list.
func (u *ui) endSimilar() {
	if u.similar == nil {
		return
	}
	u.applyFilter(u.leaveSimilar())
}
