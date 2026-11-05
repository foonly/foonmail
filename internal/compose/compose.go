// Package compose builds outgoing messages: drafts for new mail, replies
// and forwards, the RFC 5322 message itself, and editing a draft in an
// external editor.
package compose

import (
	"bytes"
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

// Attachment is a file to send: from disk (Path) or already in memory
// (Data, as when forwarding).
type Attachment struct {
	Name        string
	ContentType string // guessed from the name if empty
	Path        string
	Data        []byte
}

// Size is the attachment's size in bytes, or -1 if its file can't be read.
func (a Attachment) Size() int64 {
	if a.Path == "" {
		return int64(len(a.Data))
	}
	fi, err := os.Stat(a.Path)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (a Attachment) read() ([]byte, error) {
	if a.Path == "" {
		return a.Data, nil
	}
	return os.ReadFile(a.Path)
}

// Draft is a message being written.
type Draft struct {
	From        string // "Name <addr>"
	To, Cc, Bcc string // comma-separated address lists, as typed
	Subject     string
	Body        string
	InReplyTo   string   // Message-ID without <>
	References  []string // without <>
	Attachments []Attachment
}

// Empty reports whether nothing has been written yet.
func (d *Draft) Empty() bool {
	return strings.TrimSpace(d.To+d.Cc+d.Bcc+d.Subject+d.Body) == "" && len(d.Attachments) == 0
}

// Build renders the draft as an RFC 5322 message: a UTF-8 format=flowed
// text part (see encodeFlowed), plus
// multipart/mixed with the attachments when there are any. Bcc is kept;
// the daemon leaves it out of what goes over the wire. Date and Message-ID
// are left to the daemon.
func Build(d *Draft) ([]byte, error) { return build(d, true) }

func build(d *Draft, strict bool) ([]byte, error) {
	from, err := mail.ParseAddress(d.From)
	if err != nil {
		return nil, fmt.Errorf("From: %w", err)
	}
	var buf bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	header("From", from.String())
	nrcpt := 0
	for _, f := range []struct{ key, value string }{{"To", d.To}, {"Cc", d.Cc}, {"Bcc", d.Bcc}} {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		if f.value == "undisclosed-recipients:;" && !strict {
			header(f.key, f.value)
			continue
		}
		list, err := mail.ParseAddressList(f.value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.key, err)
		}
		nrcpt += len(list)
		header(f.key, joinAddresses(list))
	}
	if nrcpt == 0 && strict {
		return nil, errors.New("there are no recipients")
	}
	header("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	if d.InReplyTo != "" {
		header("In-Reply-To", "<"+d.InReplyTo+">")
	}
	if len(d.References) > 0 {
		refs := make([]string, len(d.References))
		for i, r := range d.References {
			refs[i] = "<" + r + ">"
		}
		header("References", strings.Join(refs, "\r\n "))
	}
	header("MIME-Version", "1.0")
	header("User-Agent", "foonmail")

	if len(d.Attachments) == 0 {
		header("Content-Type", "text/plain; charset=utf-8; format=flowed")
		header("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		if err := writeQP(&buf, encodeFlowed(d.Body)); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	header("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	buf.WriteString("\r\n")
	pw, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8; format=flowed"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	if err := writeQP(pw, encodeFlowed(d.Body)); err != nil {
		return nil, err
	}
	for _, a := range d.Attachments {
		data, err := a.read()
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", a.Name, err)
		}
		name := a.Name
		if name == "" {
			name = filepath.Base(a.Path)
		}
		ctype := a.ContentType
		if ctype == "" {
			ctype = mime.TypeByExtension(filepath.Ext(name))
		}
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		// TypeByExtension can include parameters ("text/plain; charset=utf-8").
		mediaType, params, err := mime.ParseMediaType(ctype)
		if err != nil {
			mediaType, params = "application/octet-stream", map[string]string{}
		}
		params["name"] = name
		ct := mime.FormatMediaType(mediaType, params)
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
		writeBase64(aw, data)
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
		s[i] = a.String() // encodes non-ASCII names
	}
	return strings.Join(s, ", ")
}

func writeQP(w io.Writer, body string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(body)); err != nil {
		return err
	}
	return qp.Close()
}

func writeBase64(w io.Writer, data []byte) {
	const line = 76
	enc := make([]byte, base64Len(len(data)))
	b64.Encode(enc, data)
	for len(enc) > line {
		w.Write(enc[:line])
		w.Write([]byte("\r\n"))
		enc = enc[line:]
	}
	if len(enc) > 0 {
		w.Write(enc)
		w.Write([]byte("\r\n"))
	}
}

// formatDate is how attributions and forward headers show a date.
func formatDate(t time.Time) string {
	return t.Local().Format("Mon, 2 Jan 2006 at 15:04")
}

// BuildDraft renders a draft for the Drafts folder. Unlike Build it accepts
// a draft without recipients, and keeps address fields that don't parse
// yet as typed, so nothing written is lost.
func BuildDraft(d *Draft) ([]byte, error) {
	lax := *d
	var keep []string
	for _, f := range []struct {
		key   string
		value *string
	}{{"To", &lax.To}, {"Cc", &lax.Cc}, {"Bcc", &lax.Bcc}} {
		v := strings.TrimSpace(*f.value)
		if v == "" {
			continue
		}
		if _, err := mail.ParseAddressList(v); err != nil {
			keep = append(keep, f.key+": "+oneLine(v))
			*f.value = ""
		}
	}
	if _, err := mail.ParseAddress(lax.From); err != nil {
		return nil, fmt.Errorf("From: %w", err)
	}
	if strings.TrimSpace(lax.To+lax.Cc+lax.Bcc) == "" {
		// Build insists on a recipient; a draft doesn't need one.
		lax.To = "undisclosed-recipients:;"
	}
	raw, err := build(&lax, false)
	if err != nil {
		return nil, err
	}
	if len(keep) == 0 {
		return raw, nil
	}
	// Unparsed fields go back in as written, in X- headers FromMessage reads.
	var extra bytes.Buffer
	for _, k := range keep {
		key, value, _ := strings.Cut(k, ": ")
		fmt.Fprintf(&extra, "X-Foonmail-Draft-%s: %s\r\n", key, mime.QEncoding.Encode("utf-8", value))
	}
	return append(extra.Bytes(), raw...), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
