package render

import "testing"

func TestClaimedTarget(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Common redirectors.
		{"https://www.google.com/url?q=https://shop.example.com/sale&sa=D", "https://shop.example.com/sale"},
		{"https://l.facebook.com/l.php?u=https%3A%2F%2Fnews.example.org%2Fstory%3Fid%3D7&h=AT0", "https://news.example.org/story?id=7"},
		{"https://eur01.safelinks.protection.outlook.com/?url=https%3A%2F%2Fintranet.example.fi%2Fdoc&data=05%7C01", "https://intranet.example.fi/doc"},
		// Double-encoded, in a parameter with an unusual name.
		{"https://click.mailer.example/c?id=9&dst=https%253A%252F%252Fshop.example.com%252Fitem", "https://shop.example.com/item"},
		// In the path.
		{"https://track.example.net/redirect/https%3A%2F%2Fshop.example.com%2Fsale", "https://shop.example.com/sale"},
		// Not claims: no embedded URL, same host, or not http(s).
		{"https://click.mailer.example/ls/click?upn=u001.AbCdEf-2Flong", ""},
		{"https://example.com/login?next=https://example.com/account", ""},
		{"https://example.com/share?u=javascript:alert(1)", ""},
		{"mailto:hi@example.com", ""},
	}
	for _, c := range cases {
		got, ok := ClaimedTarget(c.in)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("ClaimedTarget(%q) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}

func TestShortLink(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"https://www.google.com/url?q=https://shop.example.com/sale&sa=D", 60, "www.google.com → shop.example.com/sale"},
		{"https://click.mailer.example/c?url=https%3A%2F%2Fshop.example.com%2Fcollections%2Fwinter-sale%3Futm%3Dx", 50,
			"click.mailer.example → shop.example.com/collectio…"},
		{"http://tracker.example/r?url=https%3A%2F%2Fshop.example.com%2F", 60, "http://tracker.example → shop.example.com"},
		{"https://example.com/docs", 60, "example.com/docs"},
	}
	for _, c := range cases {
		if got := ShortLink(c.in, c.max); got != c.want {
			t.Errorf("ShortLink(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}
