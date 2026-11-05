package render

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// TextSpan is a byte range of Message.Text.
type TextSpan struct {
	Start, End int
}

// While text is built, link spans are marked with private-use characters,
// which survive wrapping, quote prefixes and tidying; extractSpans then
// removes them and records the positions. A span opened with markStart
// ends with markEnd, or is dropped with markCancel.
const (
	markStart  = string(rune(0xE000))
	markEnd    = string(rune(0xE001))
	markCancel = string(rune(0xE002))
)

var markStripper = strings.NewReplacer(markStart, "", markEnd, "", markCancel, "")

// stripMarks removes marker characters arriving in mail, so they can't
// fake a link span.
func stripMarks(s string) string { return markStripper.Replace(s) }

// extractSpans removes the marks from text and returns the spans they
// enclosed.
func extractSpans(text string) (string, []TextSpan) {
	if !strings.ContainsAny(text, markStart+markEnd+markCancel) {
		return text, nil
	}
	var b strings.Builder
	var spans []TextSpan
	var open []int
	for _, r := range text {
		switch string(r) {
		case markStart:
			open = append(open, b.Len())
		case markEnd, markCancel:
			if len(open) == 0 {
				continue
			}
			start := open[len(open)-1]
			open = open[:len(open)-1]
			if string(r) == markEnd && b.Len() > start {
				spans = append(spans, TextSpan{Start: start, End: b.Len()})
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), spans
}

// shownURLLength is how long a web address in plain text may be before it
// is shortened in the text (it stays whole in the link list).
const shownURLLength = 40

var plainURL = regexp.MustCompile(`<?(?:https?://|www\.)[^\s<>"'` + "`" + `]+>?`)

// numberer numbers a message's links: one number per address, reusing
// the numbers of earlier messages in the conversation, new ones after the
// highest so far.
type numberer struct {
	nums  map[string]int
	next  int
	links []Link // this message's, in order of first appearance
	seen  map[string]bool
}

func newNumberer(opts Options) *numberer {
	n := &numberer{nums: map[string]int{}, seen: map[string]bool{}, next: opts.LinkOffset + 1}
	for _, l := range opts.Earlier {
		if _, ok := n.nums[l.URL]; !ok {
			n.nums[l.URL] = l.N
		}
		n.next = max(n.next, l.N+1)
	}
	return n
}

// number returns the address's number, listing it among this message's
// links the first time it appears here.
func (n *numberer) number(url string) int {
	num, ok := n.nums[url]
	if !ok {
		num = n.next
		n.next++
		n.nums[url] = num
	}
	if !n.seen[url] {
		n.seen[url] = true
		n.links = append(n.links, Link{N: num, URL: url})
	}
	return num
}

// linkifyPlain numbers the web addresses in plain text like the links of
// converted HTML: each gets "[N]" after it and an entry in the link list,
// and long ones are shortened in the text, keeping their host. The
// addresses are marked as link spans.
func linkifyPlain(text string, nums *numberer) (string, []Link) {
	text = stripMarks(text)
	var b strings.Builder
	last := 0
	for _, loc := range plainURL.FindAllStringIndex(text, -1) {
		m := text[loc[0]:loc[1]]
		bracketed := strings.HasPrefix(m, "<") && strings.HasSuffix(m, ">")
		if strings.HasPrefix(m, "<") && !bracketed {
			continue // a stray <, not an address in brackets
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(m, "<"), ">")
		url, tail := trimURL(raw)
		if bracketed {
			tail = "" // the closing > belongs to the address
		}
		target := url
		if strings.HasPrefix(target, "www.") {
			target = "https://" + target
		}
		shown := url
		if utf8.RuneCountInString(url) > shownURLLength {
			shown = ShortURL(target, shownURLLength)
		}
		b.WriteString(text[last:loc[0]])
		// "Android<https://…>": the brackets kept the address apart.
		if bracketed && loc[0] > 0 && !strings.ContainsAny(text[loc[0]-1:loc[0]], " \t\n(") {
			b.WriteByte(' ')
		}
		b.WriteString(markStart + shown + " [" + strconv.Itoa(nums.number(target)) + "]" + markEnd + tail)
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String(), nums.links
}

// trimURL separates punctuation that ends a sentence from an address, and
// a closing bracket the address didn't open.
func trimURL(s string) (url, tail string) {
	for len(s) > 0 {
		last := s[len(s)-1]
		switch {
		case strings.IndexByte(".,;:!?'\"", last) >= 0:
		case last == ')' && strings.Count(s, "(") < strings.Count(s, ")"):
		case last == ']' && strings.Count(s, "[") < strings.Count(s, "]"):
		default:
			return s, tail
		}
		tail = string(last) + tail
		s = s[:len(s)-1]
	}
	return s, tail
}

// tabWidth is the distance between tab stops.
const tabWidth = 8

// expandTabs replaces tabs with spaces to the next tab stop. Text widgets
// don't lay tabs out; they show as a missing glyph. The link marks take no
// column.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch {
		case r == '\t':
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r == '\n':
			b.WriteRune(r)
			col = 0
		case strings.ContainsRune(markStart+markEnd+markCancel, r):
			b.WriteRune(r)
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}
