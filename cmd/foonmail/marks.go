package main

import "maps"

// Marks select several conversations in the list for a, s, d, v and N.
// They are kept by conversation key, so they survive the list being
// reloaded or filtered; a conversation that leaves the folder drops its
// mark.

// clearMarks drops every mark and ends a range.
func (u *ui) clearMarks() {
	clear(u.marks)
	u.anchor = -1
}

// extendTo moves the selection to row i, marking the range from where the
// range started (the first Shift+move) to i, on top of the marks made
// before it.
func (u *ui) extendTo(i int) {
	if len(u.view) == 0 {
		return
	}
	if u.anchor < 0 {
		u.anchor = u.sel
		u.markBase = maps.Clone(u.marks)
	}
	u.moveTo(i)
	clear(u.marks)
	maps.Copy(u.marks, u.markBase)
	lo, hi := min(u.anchor, u.sel), max(u.anchor, u.sel)
	for r := lo; r <= hi && r < len(u.view); r++ {
		u.marks[u.convs[u.view[r]].key] = true
	}
}

// toggleMark marks or unmarks one row, ending a range.
func (u *ui) toggleMark(i int) {
	if i < 0 || i >= len(u.view) {
		return
	}
	k := u.convs[u.view[i]].key
	if u.marks[k] {
		delete(u.marks, k)
	} else {
		u.marks[k] = true
	}
	u.anchor = -1
}

// pruneMarks drops the marks of conversations no longer in the folder.
func (u *ui) pruneMarks() {
	if len(u.marks) == 0 {
		return
	}
	here := make(map[string]bool, len(u.convs))
	for _, c := range u.convs {
		here[c.key] = true
	}
	maps.DeleteFunc(u.marks, func(k string, _ bool) bool { return !here[k] })
	if u.anchor >= len(u.view) {
		u.anchor = -1
	}
}

// markCount is how many shown conversations are marked.
func (u *ui) markCount() int {
	n := 0
	for _, i := range u.view {
		if u.marks[u.convs[i].key] {
			n++
		}
	}
	return n
}
