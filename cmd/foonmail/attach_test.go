package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
)

const withAttachments = "From: Bob <bob@example.com>\r\nSubject: Invoice\r\nMessage-ID: <x@test>\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: multipart/related; boundary=r\r\n\r\n" +
	"--r\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p><img src=\"cid:logo@x\"> Pay it.</p>\r\n" +
	"--r\r\nContent-Type: image/png\r\nContent-ID: <logo@x>\r\nContent-Disposition: inline\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0KGgo=\r\n" +
	"--r--\r\n" +
	"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"invoice.pdf\"\r\n\r\n%PDF-1.4 invoice\r\n" +
	"--b\r\nContent-Type: application/x-desktop\r\nContent-Disposition: attachment; filename=\"run.desktop\"\r\n\r\n[Desktop Entry]\r\n" +
	"--b--\r\n"

func openInvoice(t *testing.T) *harness {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	downloads := t.TempDir()
	h := newHarness(t)
	h.u.downloads = func() string { return downloads }
	h.daemon.raw[h.ids["x"]] = []byte(withAttachments)
	h.press(key.NameReturn)
	h.eventually("loaded", h.loaded)
	h.press(key.NameRightArrow)
	h.eventually("the invoice", func() bool { return h.u.conv.conv.key == "x" && h.loaded() })
	return h
}

func TestOpenAndSaveAttachments(t *testing.T) {
	h := openInvoice(t)
	if got := attachmentLines(h.u.conv.parts[0].rendered); len(got) != 2 || !strings.HasPrefix(got[0], "#1 invoice.pdf") {
		t.Fatalf("attachments listed: %q", got)
	}

	// Two attachments: o needs a number.
	h.press("O")
	if !strings.Contains(h.u.notice, "number (1–2)") || len(h.opened) != 0 {
		t.Errorf("o without a number: notice %q, opened %v", h.u.notice, h.opened)
	}

	h.press("1")
	h.press("O")
	h.eventually("opened", func() bool { return len(h.opened) == 1 })
	path := h.opened[0]
	if filepath.Base(path) != "invoice.pdf" || !strings.HasPrefix(path, os.Getenv("XDG_RUNTIME_DIR")) {
		t.Errorf("opened %s", path)
	}
	if data, _ := os.ReadFile(path); string(data) != "%PDF-1.4 invoice" {
		t.Errorf("opened file holds %q", data)
	}

	// A launcher is never handed to the opener, but can be saved.
	h.press("2")
	h.press("O")
	if len(h.opened) != 1 || !strings.Contains(h.u.notice, "Not opening run.desktop") {
		t.Errorf("risky file: opened %v, notice %q", h.opened, h.u.notice)
	}

	// Saving again doesn't replace the first copy.
	for _, name := range []string{"invoice.pdf", "invoice (2).pdf"} {
		h.press("1")
		h.press("W")
		saved := filepath.Join(h.u.downloads(), name)
		h.eventually("saved "+name, func() bool { return h.u.notice == "Saved "+saved })
		if info, err := os.Stat(saved); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", name, info, err)
		}
	}

	h.press("9")
	h.press("W")
	if h.u.notice != "No attachment #9 in this message" {
		t.Errorf("notice %q", h.u.notice)
	}
}

func TestOpenHTMLInBrowser(t *testing.T) {
	h := openInvoice(t)
	h.press("B")
	h.eventually("opened", func() bool { return len(h.opened) == 1 })
	page, err := os.ReadFile(h.opened[0])
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(h.opened[0]) != "Invoice.html" || !strings.Contains(string(page), "Content-Security-Policy") ||
		!strings.Contains(string(page), "data:image/png;base64,iVBORw0KGgo=") {
		t.Errorf("%s:\n%s", h.opened[0], page)
	}

	// A plain-text message has nothing to show.
	h.press(key.NameLeftArrow)
	h.eventually("previous conversation", func() bool { return h.u.conv.conv.key != "x" && h.loaded() })
	h.u.conv.cur = 1 // the plain reply in Sent
	h.press("B")
	if h.u.notice != "This message has no HTML version" || len(h.opened) != 1 {
		t.Errorf("notice %q, opened %v", h.u.notice, h.opened)
	}
}
