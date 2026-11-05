package render

import (
	"strings"
	"testing"
)

// mixedWithImage is a message with HTML that shows an embedded image, and
// an attached PDF.
func mixedWithImage() []byte {
	return []byte(strings.ReplaceAll(`From: Bob <bob@example.com>
Subject: Receipt
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary=outer

--outer
Content-Type: multipart/related; boundary=rel

--rel
Content-Type: text/html; charset=iso-8859-1

<html><head><meta charset="iso-8859-1"><script>alert(1)</script></head><body><img src="cid:logo@x"> Kiitos k=E4ynnist=E4 <img src="https://tracker.example/p.gif"></body></html>
--rel
Content-Type: image/png
Content-ID: <logo@x>
Content-Disposition: inline
Content-Transfer-Encoding: base64

iVBORw0KGgo=
--rel--

--outer
Content-Type: application/pdf; name="receipt.pdf"
Content-Disposition: attachment; filename="receipt.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQK
--outer--
`, "\n", "\r\n"))
}

func TestBrowserPage(t *testing.T) {
	raw := []byte(strings.Replace(string(mixedWithImage()), "Content-Type: text/html; charset=iso-8859-1",
		"Content-Type: text/html; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable", 1))
	m := mustRender(t, raw, Options{})
	page, err := BrowserPage(m, raw)
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	if !strings.HasPrefix(s, `<meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none';`) {
		t.Errorf("page doesn't start with the charset and policy:\n%.200s", s)
	}
	if !strings.Contains(s, `<img src="data:image/png;base64,iVBORw0KGgo=">`) || strings.Contains(s, "cid:") {
		t.Errorf("embedded image not inlined:\n%s", s)
	}
	if !strings.Contains(s, "Kiitos käynnistä") {
		t.Errorf("text not decoded to UTF-8:\n%s", s)
	}
}

func TestBrowserPageNeedsHTML(t *testing.T) {
	m := mustRender(t, raw([]string{"Content-Type: text/plain"}, "hi"), Options{})
	if _, err := BrowserPage(m, nil); err != ErrNoHTML {
		t.Errorf("err = %v", err)
	}
}

func TestRiskyToOpen(t *testing.T) {
	cases := map[Attachment]bool{
		{Filename: "receipt.pdf", ContentType: "application/pdf"}:        false,
		{Filename: "photo.JPG", ContentType: "image/jpeg"}:               false,
		{Filename: "launch.desktop", ContentType: "application/pdf"}:     true,
		{Filename: "Install.SH", ContentType: "text/plain"}:              true,
		{Filename: "page.html", ContentType: "text/html"}:                true,
		{Filename: "noext", ContentType: "application/x-executable"}:     true,
		{Filename: "../evil.AppImage", ContentType: "application/octet"}: true,
		{Filename: "drawing.svg", ContentType: "image/svg+xml"}:          true,
	}
	for a, want := range cases {
		if got := RiskyToOpen(a); got != want {
			t.Errorf("RiskyToOpen(%s, %s) = %v", a.Filename, a.ContentType, got)
		}
	}
}
