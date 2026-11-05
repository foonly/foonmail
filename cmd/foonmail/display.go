package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"foonmail/internal/cache"
	"foonmail/internal/render"
)

// spanKind says how a stretch of the shown text is colored.
type spanKind uint8

const (
	spanLink  spanKind = iota
	spanQuote          // a quoted line; depth says how deep
	spanMuted          // attributions, collapsed quotes, signatures
)

// textSpan is a byte range of the shown text and how to color it.
type textSpan struct {
	start, end int
	kind       spanKind
	depth      int // quote depth, from 1
}

// bodyText is the message text as shown: trailing quotes collapsed to one
// line unless showQuotes is set. Quotes with answers between them always
// stay visible.
func bodyText(m *render.Message, showQuotes bool) string {
	text, _ := bodyView(m, showQuotes)
	return text
}

// bodyView is bodyText with the spans to color in it. Later spans take
// precedence over earlier ones where they overlap.
func bodyView(m *render.Message, showQuotes bool) (string, []textSpan) {
	text := m.Text
	var marks []textSpan // spans found by the renderer, kept through collapsing
	for _, q := range m.Quotes {
		if q.Attribution == "" {
			continue
		}
		end := q.End
		if nl := strings.IndexByte(text[q.Start:q.End], '\n'); nl >= 0 {
			end = q.Start + nl
		}
		marks = append(marks, textSpan{start: q.Start, end: end, kind: spanMuted})
	}
	for _, l := range m.LinkSpans {
		marks = append(marks, textSpan{start: l.Start, end: l.End, kind: spanLink})
	}
	if !showQuotes {
		for i := len(m.Quotes) - 1; i >= 0; i-- {
			q := m.Quotes[i]
			if !q.Trailing {
				continue
			}
			who := q.Who
			if who == "" {
				who = "the earlier message"
			}
			line := fmt.Sprintf("[… quoted text from %s, %d lines · h shows it]", who, q.Lines)
			text = text[:q.Start] + line + text[q.End:]
			delta := len(line) - (q.End - q.Start)
			kept := marks[:0]
			for _, s := range marks {
				switch {
				case s.end <= q.Start:
				case s.start >= q.End:
					s.start += delta
					s.end += delta
				default:
					continue // inside the collapsed quote
				}
				kept = append(kept, s)
			}
			marks = append(kept, textSpan{start: q.Start, end: q.Start + len(line), kind: spanMuted})
		}
	}
	return text, append(lineSpans(text), marks...)
}

// lineSpans finds quoted lines and the signature in shown text.
func lineSpans(text string) []textSpan {
	var spans []textSpan
	sig := -1 // where an open signature started
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}
		line := text[start:end]
		if d := quoteDepth(line); d > 0 {
			if sig >= 0 {
				spans = append(spans, textSpan{start: sig, end: start, kind: spanMuted})
				sig = -1
			}
			spans = append(spans, textSpan{start: start, end: end, kind: spanQuote, depth: d})
		} else if sig < 0 && (line == "-- " || line == "--") {
			sig = start
		}
		start = end + 1
	}
	if sig >= 0 {
		spans = append(spans, textSpan{start: sig, end: len(text), kind: spanMuted})
	}
	return spans
}

// quoteDepth counts the ">" a line starts with, spaces between them
// allowed.
func quoteDepth(line string) int {
	d := 0
	for _, c := range []byte(line) {
		switch c {
		case '>':
			d++
		case ' ':
			if d == 0 {
				return 0
			}
		default:
			return d
		}
	}
	return d
}

// hasCollapsible reports whether h would change anything.
func hasCollapsible(m *render.Message) bool {
	return slices.ContainsFunc(m.Quotes, func(q render.Quote) bool { return q.Trailing })
}

// linkLines lists the numbered links, each fitting width characters.
func linkLines(links []render.Link, width int) []string {
	out := make([]string, len(links))
	for i, l := range links {
		prefix := fmt.Sprintf("[%d] ", l.N)
		out[i] = prefix + render.ShortLink(l.URL, max(20, width-len(prefix)))
	}
	return out
}

func attachmentLines(atts []render.Attachment) []string {
	var out []string
	for _, a := range atts {
		if a.Inline && strings.HasPrefix(a.ContentType, "image/") {
			continue // embedded images belong to the HTML, not the attachment list
		}
		out = append(out, fmt.Sprintf("%s  (%s, %s)", render.SafeFilename(a.Filename), a.ContentType, humanSize(a.Size)))
	}
	return out
}

func addressList(as []render.Address) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

// linkURL returns the URL for a typed link number, if it is one that may be
// opened: web and mail links only.
func linkURL(links []render.Link, typed string) (string, bool) {
	for _, l := range links {
		if fmt.Sprint(l.N) == typed {
			lower := strings.ToLower(l.URL)
			if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "mailto:") {
				return l.URL, true
			}
			return "", false
		}
	}
	return "", false
}

func unread(s cache.Summary) bool  { return !slices.Contains(s.Flags, `\Seen`) }
func flagged(s cache.Summary) bool { return slices.Contains(s.Flags, `\Flagged`) }

// setFlag returns flags with f added or removed, sorted like the cache's.
func setFlag(flags []string, f string, on bool) []string {
	out := slices.DeleteFunc(slices.Clone(flags), func(x string) bool { return x == f })
	if on {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

func senderName(a cache.Address) string {
	switch {
	case a.Name != "":
		return a.Name
	case a.Addr != "":
		return a.Addr
	}
	return "(unknown sender)"
}

// shortDate is the list's date column: time today, day and month this year,
// the full date before that.
func shortDate(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t = t.Local()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case y1 == y2:
		return t.Format("2 Jan")
	default:
		return t.Format("2006-01-02")
	}
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// displayName is an address's name, or the address when it has none.
func displayName(a render.Address) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Addr
}
