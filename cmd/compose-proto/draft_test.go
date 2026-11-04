package main

import (
	"bytes"
	"io"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDraftRoundTrip(t *testing.T) {
	att := filepath.Join(t.TempDir(), "liite ä.txt")
	if err := os.WriteFile(att, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := draft{
		To:      "Bob <bob@example.com>",
		Subject: "Re: Kahvitauko torstaina?",
		Attach:  []string{att},
		Body:    "Hei!\n\n> quoted\n> Attach: /not/a/header",
	}
	out, problems := parseDraft(formatDraft(in))
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	if out.To != in.To || out.Subject != in.Subject || out.Body != in.Body {
		t.Errorf("round trip mismatch:\n got %#v\nwant %#v", out, in)
	}
	if len(out.Attach) != 1 || out.Attach[0] != att {
		t.Errorf("attachments = %v, want [%s]", out.Attach, att)
	}
}

func TestParseDraftProblems(t *testing.T) {
	src := "To: a@example.com\n" +
		"Attach: /definitely/missing.pdf\n" +
		"Attach: relative.pdf\n" +
		"Bcc: nope@example.com\n" +
		"not a header\n" +
		"\nbody\n"
	_, problems := parseDraft([]byte(src))
	want := []string{"attachment not found", "must be absolute", `unknown header "Bcc"`, "is not a header"}
	if len(problems) != len(want) {
		t.Fatalf("got %d problems %q, want %d", len(problems), problems, len(want))
	}
	for i, w := range want {
		if !strings.Contains(problems[i], w) {
			t.Errorf("problem %d = %q, want it to contain %q", i, problems[i], w)
		}
	}
}

func TestParseDraftFoldedHeader(t *testing.T) {
	d, problems := parseDraft([]byte("To: a@example.com,\n  b@example.com\n\nbody\n"))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if d.To != "a@example.com, b@example.com" {
		t.Errorf("To = %q", d.To)
	}
}

func TestAnnotateReplacesOldProblems(t *testing.T) {
	edited := []byte(problemPrefix + "old problem\nTo: x\n\n" + problemPrefix + "in body stays\n")
	got := string(annotate(edited, []string{"new problem"}))
	want := problemPrefix + "new problem\nTo: x\n\n" + problemPrefix + "in body stays\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestBuildMessageWithAttachment(t *testing.T) {
	att := filepath.Join(t.TempDir(), "kuva ö.png")
	if err := os.WriteFile(att, bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	d := draft{
		To:      "Örjan Ääkkönen <orjan@example.com>",
		Subject: "Päivän kahvit",
		Attach:  []string{att},
		Body:    "Hei, tässä kuva.",
	}
	raw, err := buildMessage("Me <me@example.com>", d, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	if s, _ := dec.DecodeHeader(msg.Header.Get("Subject")); s != d.Subject {
		t.Errorf("Subject decodes to %q", s)
	}
	to, err := msg.Header.AddressList("To")
	if err != nil || to[0].Name != "Örjan Ääkkönen" {
		t.Errorf("To = %v, %v", to, err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q", msg.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(msg.Body)
	if !strings.Contains(string(body), "filename*=utf-8''kuva%20%C3%B6.png") {
		t.Errorf("attachment filename not RFC 2231 encoded:\n%s", body)
	}
}
