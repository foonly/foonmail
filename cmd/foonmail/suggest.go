package main

import (
	"cmp"
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/text/unicode/norm"

	"foonmail/internal/cache"
)

// contact is a correspondent ranked for address suggestions.
type contact struct {
	name, addr string
	score      float64
	words      []string // folded name words, for matching
	folded     string   // folded address
}

// automated matches addresses that don't read replies, which are only
// suggested if you have written to them.
var automated = regexp.MustCompile(`(?i)(no-?reply|do-?not-?reply|mailer-daemon|bounce|notifications?@)`)

// rankContacts scores correspondents: people you write to count more than
// people who write to you, and recent mail more than old.
func rankContacts(cs []cache.Correspondent, now time.Time) []contact {
	out := make([]contact, 0, len(cs))
	for _, c := range cs {
		if c.Sent == 0 && automated.MatchString(c.Addr) {
			continue
		}
		days := now.Sub(c.Last).Hours() / 24
		recency := 1 / (1 + max(days, 0)/60)
		k := contact{name: c.Name, addr: c.Addr, score: float64(5*c.Sent+c.Received) * recency}
		k.words, k.folded = strings.Fields(fold(c.Name)), fold(c.Addr)
		out = append(out, k)
	}
	slices.SortStableFunc(out, func(a, b contact) int { return cmp.Compare(b.score, a.score) })
	return out
}

// fold lowercases and drops accents, so "strom" finds "Ström".
func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// String is the contact as it goes in an address field.
func (c contact) String() string {
	if c.name == "" {
		return c.addr
	}
	if strings.ContainsAny(c.name, `,;<>"@()`) {
		return `"` + strings.ReplaceAll(c.name, `"`, `'`) + `" <` + c.addr + ">"
	}
	return c.name + " <" + c.addr + ">"
}

// matches reports whether typed (folded) starts the contact's address, or
// its typed words start words of its name, in order: "kim st" and "strom"
// find "Kim Ström".
func (c contact) matches(typed string) bool {
	parts := strings.Fields(typed)
	if len(parts) == 0 {
		return false
	}
	if strings.HasPrefix(c.folded, typed) {
		return true
	}
	i := 0
	for _, w := range c.words {
		if i < len(parts) && strings.HasPrefix(w, parts[i]) {
			i++
		}
	}
	return i == len(parts)
}

// suggestions are the best contacts matching what is typed, leaving out
// addresses already in the field.
func suggestions(contacts []contact, typed string, already []string, limit int) []contact {
	typed = fold(strings.TrimSpace(typed))
	if typed == "" {
		return nil
	}
	skip := map[string]bool{}
	for _, a := range already {
		skip[strings.ToLower(a)] = true
	}
	var out []contact
	for _, c := range contacts {
		if !skip[strings.ToLower(c.addr)] && c.matches(typed) {
			out = append(out, c)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// currentEntry finds the address being typed in a field: the text from the
// last comma before the caret to the caret. caret and start count runes,
// as widget.Editor does.
func currentEntry(text string, caret int) (entry string, start int) {
	runes := []rune(text)
	caret = min(max(caret, 0), len(runes))
	start = caret
	for start > 0 && runes[start-1] != ',' && runes[start-1] != ';' {
		start--
	}
	return string(runes[start:caret]), start
}

// addressesIn lists the addresses already in a field.
var fieldAddr = regexp.MustCompile(`[^\s,;<>"]+@[^\s,;<>"]+`)

func addressesIn(text string) []string { return fieldAddr.FindAllString(text, -1) }

// loadContacts ranks the account's correspondents in the background, for
// address suggestions; it runs each time compose opens, so new mail counts.
func (u *ui) loadContacts() {
	if u.db == nil {
		return
	}
	db, account, own := u.db, u.accountID, u.own
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cs, err := db.Correspondents(ctx, account, own)
		if err != nil {
			u.log.Debug("loading contacts", "err", err)
			return
		}
		ranked := rankContacts(cs, time.Now())
		u.post(func() { u.contacts = ranked })
	}()
}

// suggestionKeys handles the keys of an open suggestion list. They are
// taken before the address field's own handling: the field would move its
// caret on ↑/↓.
func (u *ui) suggestionKeys(gtx layout.Context) {
	c := u.comp
	f := c.suggField
	if len(c.sugg) == 0 || f == nil {
		return
	}
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: f, Name: key.NameDownArrow},
			key.Filter{Focus: f, Name: key.NameUpArrow},
			key.Filter{Focus: f, Name: key.NameTab},
			key.Filter{Focus: f, Name: key.NameReturn},
			key.Filter{Focus: f, Name: key.NameEnter},
			key.Filter{Focus: f, Name: key.NameEscape},
		)
		if !ok {
			return
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press || len(c.sugg) == 0 {
			continue
		}
		n := len(c.sugg)
		switch ke.Name {
		case key.NameDownArrow:
			c.suggSel = (c.suggSel + 1) % n
		case key.NameUpArrow:
			c.suggSel = (c.suggSel + n - 1) % n
		case key.NameTab, key.NameReturn, key.NameEnter:
			u.acceptSuggestion()
		case key.NameEscape:
			c.suggOff, c.sugg = true, nil
		}
	}
}

// updateSuggestions finds suggestions for the entry being typed in the
// focused address field.
func (u *ui) updateSuggestions(gtx layout.Context) {
	c := u.comp
	var f *widget.Editor
	for _, e := range []*widget.Editor{&c.to, &c.cc, &c.bcc} {
		if gtx.Source.Focused(e) {
			f = e
		}
	}
	if f == nil || c.suggOff || c.busy || f.SelectionLen() > 0 {
		c.sugg, c.suggField, c.suggEntry = nil, nil, ""
		return
	}
	_, caret := f.Selection()
	entry, _ := currentEntry(f.Text(), caret)
	if f != c.suggField || entry != c.suggEntry {
		c.suggSel = 0
	}
	c.sugg = suggestions(u.contacts, entry, addressesIn(f.Text()), 6)
	c.suggField, c.suggEntry = f, entry
	c.suggSel = min(c.suggSel, max(len(c.sugg)-1, 0))
}

// acceptSuggestion puts the chosen address in place of the entry being
// typed, followed by ", " for the next one.
func (u *ui) acceptSuggestion() {
	c := u.comp
	f, pick := c.suggField, c.sugg[c.suggSel]
	_, caret := f.Selection()
	text := f.Text()
	_, start := currentEntry(text, caret)
	runes := []rune(text)
	prefix := strings.TrimRight(string(runes[:start]), " ")
	if prefix != "" {
		prefix += " "
	}
	filled := prefix + pick.String() + ", "
	f.SetText(filled + strings.TrimLeft(string(runes[min(caret, len(runes)):]), " ,;"))
	n := utf8.RuneCountInString(filled)
	f.SetCaret(n, n)
	c.sugg, c.suggEntry = nil, ""
	c.edited(u.now())
}

// suggestionList shows the suggestions under the field they are for.
func (u *ui) suggestionList(f *widget.Editor) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		c := u.comp
		if c.suggField != f || len(c.sugg) == 0 {
			return layout.Dimensions{}
		}
		var rows []layout.FlexChild
		for i, s := range c.sugg {
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				m := op.Record(gtx.Ops)
				d := layout.Inset{Top: 3, Bottom: 3, Left: 8, Right: 8}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							if s.name == "" {
								return layout.Dimensions{}
							}
							l := material.Label(u.th, u.th.TextSize, s.name+"  ")
							l.MaxLines = 1
							return l.Layout(gtx)
						}),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							l := material.Label(u.th, u.th.TextSize*0.9, s.addr)
							l.Color, l.MaxLines = u.muted, 1
							if s.name == "" {
								l.Color = u.th.Fg
							}
							return l.Layout(gtx)
						}),
					)
				})
				call := m.Stop()
				if i == c.suggSel {
					paint.FillShape(gtx.Ops, u.selBg, clip.Rect{Max: d.Size}.Op())
				}
				call.Add(gtx.Ops)
				return d
			}))
		}
		return layout.Inset{Left: 88, Right: 12, Bottom: 4}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	}
}
