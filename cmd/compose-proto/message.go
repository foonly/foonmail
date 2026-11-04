package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildMessage renders a draft as an RFC 5322 message with a plain-text
// body, plus attachments as multipart/mixed when there are any.
func buildMessage(from string, d draft, now time.Time) ([]byte, error) {
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("from address %q: %w", from, err)
	}
	if strings.TrimSpace(d.To) == "" {
		return nil, errors.New("no recipients in To")
	}
	to, err := mail.ParseAddressList(d.To)
	if err != nil {
		return nil, fmt.Errorf("To: %w", err)
	}
	var cc []*mail.Address
	if strings.TrimSpace(d.Cc) != "" {
		if cc, err = mail.ParseAddressList(d.Cc); err != nil {
			return nil, fmt.Errorf("Cc: %w", err)
		}
	}

	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("Date", now.Format(time.RFC1123Z))
	header("From", fromAddr.String())
	header("To", joinAddresses(to))
	if len(cc) > 0 {
		header("Cc", joinAddresses(cc))
	}
	header("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	header("Message-ID", "<"+randomHex(16)+"@foonmail.invalid>")
	header("MIME-Version", "1.0")

	if len(d.Attach) == 0 {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		if err := writeQuotedPrintable(&buf, d.Body); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	header("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	buf.WriteString("\r\n")

	pw, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	if err := writeQuotedPrintable(pw, d.Body); err != nil {
		return nil, err
	}

	for _, path := range d.Attach {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("attachment: %w", err)
		}
		name := filepath.Base(path)
		ctype := mime.TypeByExtension(filepath.Ext(name))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		// FormatMediaType applies RFC 2231 encoding to non-ASCII names.
		ct := mime.FormatMediaType(ctype, map[string]string{"name": name})
		cd := mime.FormatMediaType("attachment", map[string]string{"filename": name})
		if ct == "" || cd == "" {
			return nil, fmt.Errorf("attachment %q: can't encode its name", name)
		}
		aw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {ct},
			"Content-Disposition":       {cd},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, err
		}
		writeBase64Lines(aw, data)
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	buf.Write(parts.Bytes())
	return buf.Bytes(), nil
}

func joinAddresses(as []*mail.Address) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String() // encodes non-ASCII display names
	}
	return strings.Join(s, ", ")
}

func writeQuotedPrintable(w io.Writer, body string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(body)); err != nil {
		return err
	}
	return qp.Close()
}

// writeBase64Lines writes data as base64 wrapped at 76 characters.
func writeBase64Lines(w io.Writer, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		w.Write([]byte(enc[:76] + "\r\n"))
		enc = enc[76:]
	}
	if enc != "" {
		w.Write([]byte(enc + "\r\n"))
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
