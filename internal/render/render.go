// Package render turns a raw message into what the reader shows: decoded
// headers, plain text (the text part, or the HTML part converted to text),
// numbered links, quoted regions and a list of attachments.
//
// Plain text is preferred. The HTML part is used when there is no text part,
// when the text part is an obvious stub ("view this email in your browser"),
// or when the caller asks for it.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode ISO-8859-*, Windows-125x and other legacy charsets
	"github.com/emersion/go-message/mail"
)

// Source says where Message.Text came from.
type Source int

const (
	SourceNone  Source = iota // no readable body
	SourcePlain               // the text/plain part
	SourceHTML                // the text/html part, converted
)

func (s Source) String() string {
	return [...]string{"none", "plain", "html"}[s]
}

// Options adjust rendering.
type Options struct {
	// PreferHTML converts the HTML part even when a usable text part exists.
	PreferHTML bool
	// LinkOffset is added to link numbers, so the messages of a conversation
	// shown together get distinct numbers: the first link is LinkOffset+1.
	LinkOffset int
}

// Address is a decoded mail address.
type Address struct {
	Name, Addr string
}

func (a Address) String() string {
	if a.Name == "" {
		return a.Addr
	}
	return a.Name + " <" + a.Addr + ">"
}

// Link is a numbered link. Text marks it with "[N]" after the link text.
type Link struct {
	N   int
	URL string
}

// Attachment is a part that isn't the message body.
type Attachment struct {
	Index       int    // for AttachmentData
	Filename    string // as sent; use SafeFilename before writing it to disk
	ContentType string
	ContentID   string // for inline images referenced as cid: from HTML
	Size        int    // decoded size in bytes
	Inline      bool   // meant to be shown in the body, such as an embedded image
}

// Message is a rendered message.
type Message struct {
	Subject      string
	From, To, Cc []Address
	ReplyTo      []Address
	MessageID    string   // without <>
	References   []string // without <>, oldest first
	Date         time.Time

	Text   string // what to show; lines starting with ">" are quoted
	Source Source
	Links  []Link  // numbered links in Text (HTML only)
	Quotes []Quote // quoted regions in Text

	HasPlain, HasHTML bool
	HTML              string // the decoded HTML part, for "open in browser"
	Attachments       []Attachment

	// Warnings are problems that didn't stop rendering, such as an
	// unknown charset (the bytes are shown as they are).
	Warnings []string
}

// Render parses and renders a raw RFC 5322 message.
func Render(raw []byte, opts Options) (*Message, error) {
	m := &Message{}
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		if r == nil {
			return nil, err
		}
		m.Warnings = append(m.Warnings, err.Error())
	}
	defer r.Close()
	m.readHeader(&r.Header)

	var plain, plainFormat, plainDelSp string
	var html string
	err = walkParts(r, func(p part) error {
		switch {
		case p.body && p.mediaType == "text/plain" && !m.HasPlain:
			m.HasPlain = true
			plain = string(p.data)
			plainFormat, plainDelSp = p.params["format"], p.params["delsp"]
		case p.body && p.mediaType == "text/html" && !m.HasHTML:
			m.HasHTML = true
			html = string(p.data)
		default:
			m.Attachments = append(m.Attachments, p.attachment(len(m.Attachments)))
		}
		if p.warning != "" {
			m.Warnings = append(m.Warnings, p.warning)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	plain = normalizeNewlines(plain)
	if strings.EqualFold(plainFormat, "flowed") {
		plain = decodeFlowed(plain, strings.EqualFold(plainDelSp, "yes"))
	}
	var conv converted
	if m.HasHTML {
		m.HTML = html
		conv = htmlToText(html, opts.LinkOffset)
	}

	switch {
	case m.HasHTML && (opts.PreferHTML || !m.HasPlain || isStub(plain, conv.text)):
		m.Text, m.Source, m.Links = conv.text, SourceHTML, conv.links
	case m.HasPlain:
		m.Text, m.Source = tidy(plain), SourcePlain
	}
	m.Quotes = findQuotes(m.Text)
	return m, nil
}

// stubPhrase matches what newsletters put in a text part instead of content.
var stubPhrase = regexp.MustCompile(`(?i)view (this|it|the) (e-?mail|message|newsletter)? ?(in|on|online|with) ` +
	`(your|a|the)? ?(web )?browser|web version|(can.?t|cannot|unable to) (display|view|see)|` +
	`html (e-?mail|version|message)|näytä selaimessa|selainversio`)

// isStub reports whether a text part is too thin to stand in for the HTML:
// empty, a short "view this in your browser" note, or a few words where
// the HTML has many times more.
func isStub(plain, htmlText string) bool {
	p := strings.TrimSpace(plain)
	if p == "" {
		return true
	}
	words := len(strings.Fields(p))
	htmlWords := len(strings.Fields(htmlText))
	return (words < 40 && stubPhrase.MatchString(p) && htmlWords > words) ||
		(words < 15 && htmlWords > 4*words)
}

func (m *Message) readHeader(h *mail.Header) {
	m.Subject, _ = h.Subject()
	m.Date, _ = h.Date()
	m.From = addresses(h, "From")
	m.To = addresses(h, "To")
	m.Cc = addresses(h, "Cc")
	m.ReplyTo = addresses(h, "Reply-To")
	m.MessageID, _ = h.MessageID()
	m.References, _ = h.MsgIDList("References")
}

func addresses(h *mail.Header, key string) []Address {
	list, err := h.AddressList(key)
	if err != nil {
		// Malformed lists are common enough; show the raw value rather than nothing.
		if raw, _ := h.Text(key); raw != "" {
			return []Address{{Addr: raw}}
		}
		return nil
	}
	out := make([]Address, len(list))
	for i, a := range list {
		out[i] = Address{Name: a.Name, Addr: a.Address}
	}
	return out
}

// part is one leaf of the MIME tree.
type part struct {
	body        bool // a candidate for the message body (inline text/plain or text/html)
	mediaType   string
	params      map[string]string
	filename    string
	contentID   string
	disposition string
	data        []byte
	warning     string
}

func (p part) attachment(index int) Attachment {
	return Attachment{
		Index:       index,
		Filename:    p.filename,
		ContentType: p.mediaType,
		ContentID:   p.contentID,
		Size:        len(p.data),
		Inline:      p.disposition == "inline" || (p.disposition == "" && p.contentID != ""),
	}
}

// walkParts calls f for every leaf part, in order.
func walkParts(r *mail.Reader, f func(part) error) error {
	for {
		mp, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		var warning string
		if err != nil {
			if !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
				return err
			}
			warning = err.Error()
		}
		var p part
		p.warning = warning
		switch h := mp.Header.(type) {
		case *mail.InlineHeader:
			p.mediaType, p.params, _ = h.ContentType()
			p.disposition, _, _ = h.ContentDisposition()
			p.contentID = strings.Trim(h.Get("Content-Id"), "<> ")
			_, dparams, _ := h.ContentDisposition()
			p.filename = dparams["filename"]
			if p.filename == "" {
				p.filename = p.params["name"]
			}
			p.body = (p.mediaType == "text/plain" || p.mediaType == "text/html") && p.filename == ""
		case *mail.AttachmentHeader:
			p.mediaType, p.params, _ = h.ContentType()
			p.disposition, _, _ = h.ContentDisposition()
			p.contentID = strings.Trim(h.Get("Content-Id"), "<> ")
			p.filename, _ = h.Filename()
			if p.filename == "" {
				p.filename = p.params["name"]
			}
		}
		p.mediaType = strings.ToLower(p.mediaType)
		if p.data, err = io.ReadAll(mp.Body); err != nil {
			if !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
				return fmt.Errorf("reading %s part: %w", p.mediaType, err)
			}
			p.warning = err.Error()
		}
		if err := f(p); err != nil {
			return err
		}
	}
}

// AttachmentData returns the decoded content of attachment index, as listed
// in Message.Attachments.
func AttachmentData(raw []byte, index int) ([]byte, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		return nil, err
	}
	defer r.Close()
	n := 0
	var plainSeen, htmlSeen bool
	var found []byte
	errFound := errors.New("found")
	err = walkParts(r, func(p part) error {
		switch {
		case p.body && p.mediaType == "text/plain" && !plainSeen:
			plainSeen = true
		case p.body && p.mediaType == "text/html" && !htmlSeen:
			htmlSeen = true
		default:
			if n == index {
				found = p.data
				return errFound
			}
			n++
		}
		return nil
	})
	if errors.Is(err, errFound) {
		return found, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("no attachment %d", index)
}

// SafeFilename turns a filename chosen by the sender into one that is safe
// to create in a directory: no path, no control characters, no leading dot.
func SafeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if name == "" {
		return "attachment"
	}
	return name
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// tidy trims trailing spaces, collapses runs of blank lines and trims blank
// lines at both ends.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\u00a0")
		if l == "" {
			blank++
			continue
		}
		if blank > 0 && len(out) > 0 {
			out = append(out, "")
		}
		blank = 0
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
