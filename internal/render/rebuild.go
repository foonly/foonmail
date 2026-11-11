package render

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// Rebuilt is an earlier message of a conversation, read back from the
// quote in a reply because the message itself isn't at hand: written
// before you were added to the conversation, for example. It is only as
// good as the quote: the text may be cut, and the time is as the
// attribution line wrote it.
type Rebuilt struct {
	Who  string    // the sender's name, or their address if no name was given
	Addr string    // the sender's address, if the quote names one
	When string    // the time as written, such as "Wed, 11 Nov 2026, 16.32"
	Date time.Time // When read as a time in the local zone; zero if it couldn't be
	Text string
}

// maxRebuilt bounds how far back a quote chain is followed.
const maxRebuilt = 20

var (
	quoteHidden = regexp.MustCompile(`(?im)^\s*\[(quoted text hidden|lainattu teksti piilotettu)\]\s*$`)
	addrRe      = regexp.MustCompile(`[^\s<>()\[\]:"']+@[^\s<>()\[\]"']+\.[^\s<>()\[\]"',;]+`)
	leadingTime = regexp.MustCompile(`^\d{1,2}[.:]\d{2}(\s*[AaPp]\.?[Mm]\.?)?\s+`)
	hasDigit    = regexp.MustCompile(`\d`)
	spaces      = regexp.MustCompile(`[\s\x{202f}\x{a0}]+`)
	linkMark    = regexp.MustCompile(`\s*\[\d+\]`)
	weekdayWord = regexp.MustCompile(`^\p{L}{2,3}\.?\s+`)
	attribWord  = regexp.MustCompile(`(?i)^(on|le|am|den|el|il|op)\s+`)
)

// Rebuild reads the earlier messages of a conversation back from a reply's
// quote, oldest first: each "… wrote:" (or Outlook "From: … Sent: …")
// level of a top-posted reply becomes one message. It finds nothing when
// the reply answers between the quoted lines, since then who wrote what
// is unclear, or when the quote doesn't say who wrote it.
func Rebuild(m *Message) []Rebuilt {
	if len(m.Quotes) == 0 {
		return nil
	}
	for _, q := range m.Quotes {
		if !q.Trailing {
			return nil
		}
	}
	q := m.Quotes[len(m.Quotes)-1]
	region := m.Text[q.Start:q.End]
	var out []Rebuilt
	for range maxRebuilt {
		r, next, ok := rebuildLevel(region)
		if !ok {
			break
		}
		out = append(out, r)
		if next == "" {
			break
		}
		region = next
	}
	slices.Reverse(out)
	return out
}

// rebuildLevel reads the outermost message of a quoted region: its
// sender and time, its text, and the region quoted inside it, if any.
func rebuildLevel(region string) (r Rebuilt, next string, ok bool) {
	ls := splitLines(strings.Trim(region, "\n"))
	first := 0
	for first < len(ls) && !strings.HasPrefix(ls[first].text, ">") {
		first++
	}
	var body string
	switch {
	case first < len(ls) && first > 0:
		// "On …, Bob <bob@example.com> wrote:" above ">" lines.
		var attr []string
		for _, l := range ls[:first] {
			if t := strings.TrimSpace(l.text); t != "" {
				attr = append(attr, t)
			}
		}
		a := strings.Join(attr, " ")
		if !attributionRe.MatchString(a) {
			return r, "", false
		}
		r.Who, r.Addr, r.When = parseAttribution(a)
		body = unquote(ls[first:])
	case first == 0:
		// Quoted with no attribution: only an Outlook header inside says who.
		body = unquote(ls)
		var ok bool
		if r, body, ok = outlookHeader(body); !ok {
			return r, "", false
		}
	default:
		// Not quoted at all: Outlook's plain-text form.
		var ok bool
		if r, body, ok = outlookHeader(strings.Join(lineTexts(ls), "\n")); !ok {
			return r, "", false
		}
	}
	if r.Who == "" && r.Addr == "" {
		return r, "", false
	}
	if r.Who == "" {
		r.Who = r.Addr
	}
	r.Date = parseWhen(r.When)

	hidden := false
	if loc := quoteHidden.FindStringIndex(body); loc != nil {
		body, hidden = body[:loc[0]], true
	}
	body = tidy(body)
	r.Text = body
	if hidden {
		r.Text = strings.TrimSpace(body)
		return r, "", true
	}
	qs := findQuotes(body)
	if len(qs) == 0 {
		r.Text = strings.TrimSpace(body)
		return r, "", true
	}
	for _, q := range qs {
		if !q.Trailing {
			// Answers between quoted lines: keep the whole text, go no further.
			r.Text = strings.TrimSpace(body)
			return r, "", true
		}
	}
	q := qs[len(qs)-1]
	r.Text = strings.TrimSpace(body[:q.Start])
	return r, body[q.Start:q.End], true
}

// unquote removes one level of ">" from lines.
func unquote(ls []line) string {
	out := make([]string, len(ls))
	for i, l := range ls {
		t, ok := strings.CutPrefix(l.text, ">")
		if ok {
			t = strings.TrimPrefix(t, " ")
		}
		out[i] = t
	}
	return strings.Join(out, "\n")
}

func lineTexts(ls []line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.text
	}
	return out
}

// outlookHeader reads Outlook's "From: / Sent: / To: / Subject:" block at
// the start of text (after its separator line, if any) and returns the
// text after it.
func outlookHeader(text string) (r Rebuilt, body string, ok bool) {
	ls := splitLines(text)
	i := 0
	for i < len(ls) && strings.TrimSpace(ls[i].text) == "" {
		i++
	}
	if i < len(ls) && outlookSep.MatchString(strings.TrimSpace(ls[i].text)) {
		i++
	}
	if i >= len(ls) || !isOutlookStart(ls, i) {
		return r, "", false
	}
	for ; i < len(ls); i++ {
		t := strings.TrimSpace(ls[i].text)
		if t == "" {
			break
		}
		if m := outlookFrom.FindStringSubmatch(t); m != nil {
			v := linkMark.ReplaceAllString(m[2], "")
			r.Addr = strings.TrimPrefix(addrRe.FindString(v), "mailto:")
			r.Who = cleanName(v)
			if strings.Contains(r.Who, "@") {
				r.Who = ""
			}
		} else if outlookDate.MatchString(t) {
			_, v, _ := strings.Cut(t, ":")
			r.When = strings.TrimSpace(strings.Trim(v, "* "))
		}
	}
	if i >= len(ls) {
		return r, "", true
	}
	return r, text[ls[i].start:], true
}

// parseAttribution splits an attribution line into the sender's name and
// address and the time as written.
func parseAttribution(a string) (who, addr, when string) {
	a = linkMark.ReplaceAllString(a, "")
	addr = strings.TrimPrefix(addrRe.FindString(a), "mailto:")
	if m := verbFirst.FindStringSubmatch(a); m != nil {
		// "Den tis 3 nov. 2026 kl 14:02 skrev Anna <anna@example.se>:"
		who = cleanName(m[2])
		if strings.Contains(who, "@") {
			who = ""
		}
		when = strings.TrimSpace(a[:strings.Index(a, m[0])])
		return who, addr, trimWhen(when)
	}
	who = whoFromAttribution(a)
	if strings.Contains(who, "@") {
		who = ""
	}
	s := strings.TrimSpace(wroteTail.ReplaceAllString(a, ""))
	if loc := angleOrParen.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	} else if addr != "" {
		if i := strings.Index(s, addr); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.TrimRight(strings.TrimSpace(s), ",")
	if who != "" {
		s = strings.TrimSuffix(strings.TrimSpace(s), who)
	}
	return who, addr, trimWhen(s)
}

// trimWhen leaves the time of an attribution: "Wed, 11 Nov 2026, 16.32"
// from "On Wed, 11 Nov 2026, 16.32".
func trimWhen(s string) string {
	s = strings.TrimSpace(s)
	s = attribWord.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ","))
	s = strings.TrimSpace(strings.TrimSuffix(s, " at"))
	return s
}

var whenLayouts = []string{
	"Mon, 2 Jan 2006, 15.04",
	"Mon, 2 Jan 2006, 15:04",
	"Mon, 2 Jan 2006 at 15:04",
	"Mon, 2 Jan 2006 at 15.04",
	"Mon, Jan 2, 2006 at 3:04 PM",
	"Mon, Jan 2, 2006, 3:04 PM",
	"Mon, 2 Jan 2006 15:04",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2 Jan 2006, at 15:04",
	"2 Jan 2006 at 15:04",
	"Monday, January 2, 2006 3:04 PM",
	"Monday, January 2, 2006 3:04:05 PM",
	"Monday, January 2, 2006 15:04",
	"Monday, 2 January 2006 15:04",
	"Monday, 2 January 2006 at 15:04",
	"2.1.2006 klo 15.04",
	"2.1.2006 15.04",
	"2006-01-02 15:04",
}

// parseWhen reads an attribution's time, in the local zone unless it
// names one; the zero time if it doesn't fit a known layout.
func parseWhen(when string) time.Time {
	s := strings.TrimSpace(spaces.ReplaceAllString(when, " "))
	if s == "" || !hasDigit.MatchString(s) {
		return time.Time{}
	}
	for _, try := range []string{s, weekdayWord.ReplaceAllString(s, "")} {
		for _, layout := range whenLayouts {
			if t, err := time.ParseInLocation(layout, try, time.Local); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
