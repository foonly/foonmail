package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortURL(t *testing.T) {
	tracking := "https://click.mailer.example.com/ls/click?upn=u001.AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-2FlongTokenThatGoesOnAndOn&utm_source=newsletter"
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"https://example.com/", 60, "example.com"},
		{"https://example.com/docs/page?x=1", 60, "example.com/docs/page?x=1"},
		{"http://example.com/plain", 60, "http://example.com/plain"},
		{"mailto:hi@example.com", 60, "hi@example.com"},
		{tracking, 40, "click.mailer.example.com/ls/click?upn=u…"},
		// The host survives even when it alone is longer than the limit.
		{"https://a-very-long-subdomain.of-a-tracking-provider.example.com/x/y/z", 30,
			"a-very-long-subdomain.of-a-tracking-provider.example.com/…"},
		{"not a url at all, just text that is rather long", 20, "not a url at all, j…"},
	}
	for _, c := range cases {
		if got := ShortURL(c.in, c.max); got != c.want {
			t.Errorf("ShortURL(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
	if got := ShortURL(tracking, 40); utf8.RuneCountInString(got) != 40 || !strings.HasPrefix(got, "click.mailer.example.com") {
		t.Errorf("tracking link: %q (%d chars)", got, utf8.RuneCountInString(got))
	}
}
