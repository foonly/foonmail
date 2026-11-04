package render

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// ShortURL shortens a URL for the link list to at most max characters,
// for the long tracking links in newsletter footers. The host is never cut,
// since showing where a link really goes is the point of the list; the path
// and query are trimmed instead, ending in "…". https:// is left out as the
// expected case; any other scheme is shown. Mail links show the address.
// The full URL is still what gets opened.
func ShortURL(raw string, max int) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if err == nil && strings.EqualFold(u.Scheme, "mailto") {
			return truncate(u.Opaque, max)
		}
		return truncate(raw, max)
	}
	head := u.Host
	if !strings.EqualFold(u.Scheme, "https") {
		head = u.Scheme + "://" + head
	}
	rest := u.EscapedPath()
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	if rest == "/" {
		rest = ""
	}
	if utf8.RuneCountInString(head+rest) <= max {
		return head + rest
	}
	budget := max - utf8.RuneCountInString(head) - 1 // room for "…"
	if budget <= 0 {
		return head + "/…"
	}
	return head + truncate(rest, budget+1)
}

// truncate cuts s to at most max characters, ending in "…" if it was cut.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
