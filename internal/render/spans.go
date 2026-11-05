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

// linkifyPlain numbers the web addresses in plain text like the links of
// converted HTML: each gets "[N]" after it and an entry in the link list,
// and long ones are shortened in the text, keeping their host. The
// addresses are marked as link spans.
func linkifyPlain(text string, linkOffset int) (string, []Link) {
	var links []Link
	nums := map[string]int{}
	out := plainURL.ReplaceAllStringFunc(stripMarks(text), func(m string) string {
		bracketed := strings.HasPrefix(m, "<") && strings.HasSuffix(m, ">")
		raw := strings.TrimSuffix(strings.TrimPrefix(m, "<"), ">")
		url, tail := trimURL(raw)
		if bracketed {
			tail = "" // the closing > belongs to the address
		} else if strings.HasPrefix(m, "<") {
			return m // a stray <, not an address in brackets
		}
		target := url
		if strings.HasPrefix(target, "www.") {
			target = "https://" + target
		}
		n, ok := nums[target]
		if !ok {
			n = linkOffset + len(links) + 1
			nums[target] = n
			links = append(links, Link{N: n, URL: target})
		}
		shown := url
		if utf8.RuneCountInString(url) > shownURLLength {
			shown = ShortURL(target, shownURLLength)
		}
		return markStart + shown + " [" + strconv.Itoa(n) + "]" + markEnd + tail
	})
	return out, links
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
