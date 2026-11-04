package render

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// Query parameters redirectors commonly carry their destination in, tried
// first. Any other parameter holding a URL is accepted after these.
var targetParams = []string{"url", "u", "q", "target", "dest", "destination", "redirect", "redirect_url",
	"redirect_uri", "link", "r", "to", "goto", "next", "continue", "return", "returnurl"}

// ClaimedTarget returns the destination a redirect or tracking link carries
// inside itself, such as https://tracker.example/click?url=https%3A%2F%2Fshop.example%2F.
// It is only what the link claims: the redirector decides where the user
// really ends up. It reports false when there is no embedded http(s) URL or
// it points back to the same host.
func ClaimedTarget(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	q := u.Query()
	for _, name := range targetParams {
		for key, vals := range q {
			if strings.EqualFold(key, name) {
				if t, ok := embeddedURL(vals[0], u.Host); ok {
					return t, true
				}
			}
		}
	}
	for _, vals := range q {
		for _, v := range vals {
			if t, ok := embeddedURL(v, u.Host); ok {
				return t, true
			}
		}
	}
	// Destination in the path: /redirect/https%3A%2F%2Fshop.example%2Fsale
	path := u.EscapedPath()
	lower := strings.ToLower(path)
	for _, marker := range []string{"https%3a%2f%2f", "http%3a%2f%2f", "https://", "http://"} {
		if i := strings.Index(lower, marker); i > 0 {
			if t, ok := embeddedURL(path[i:], u.Host); ok {
				return t, true
			}
		}
	}
	return "", false
}

// embeddedURL unescapes v (redirectors often encode twice) and accepts it if
// it is an absolute http(s) URL on a host other than self.
func embeddedURL(v, self string) (string, bool) {
	for range 3 {
		lower := strings.ToLower(v)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			break
		}
		un, err := url.QueryUnescape(v)
		if err != nil || un == v {
			return "", false
		}
		v = un
	}
	t, err := url.Parse(v)
	if err != nil || t.Host == "" || (t.Scheme != "http" && t.Scheme != "https") {
		return "", false
	}
	if strings.EqualFold(t.Hostname(), strings.Split(self, ":")[0]) {
		return "", false
	}
	return v, true
}

// ShortLink is ShortURL plus the claimed destination of a redirect:
// "click.mailer.example → shop.example/sale…". The link's own host comes
// first and both hosts are always shown whole; the claim gets what room is
// left. Without a claim it is ShortURL.
func ShortLink(raw string, max int) string {
	target, ok := ClaimedTarget(raw)
	if !ok {
		return ShortURL(raw, max)
	}
	u, _ := url.Parse(raw)
	head := u.Host
	if !strings.EqualFold(u.Scheme, "https") {
		head = u.Scheme + "://" + head
	}
	head += " → "
	room := max - utf8.RuneCountInString(head)
	return head + ShortURL(target, room)
}
