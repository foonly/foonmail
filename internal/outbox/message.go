package outbox

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"foonmail/internal/config"
)

// prepared is a message ready for the outbox.
type prepared struct {
	from      string   // envelope sender (MAIL FROM)
	rcpts     []string // envelope recipients: To, Cc and Bcc
	wire      []byte   // what is sent: without Bcc
	stored    []byte   // the copy for Sent: with Bcc
	messageID string
	subject   string
	to        string // short recipient summary for notifications
}

// prepare checks a message and derives what sending needs. It fills in
// From, Date and Message-ID when they are missing, and keeps Bcc out of
// the copy that goes over the wire.
func prepare(raw []byte, acct *config.Account, now time.Time) (*prepared, error) {
	br := bufio.NewReader(bytes.NewReader(raw))
	th, err := textproto.ReadHeader(br)
	if err != nil {
		return nil, fmt.Errorf("reading the message header: %w", err)
	}
	body, err := readAll(br)
	if err != nil {
		return nil, err
	}
	h := mail.Header{Header: message.Header{Header: th}}

	own := acct.Addresses()
	from, err := h.AddressList("From")
	if err != nil {
		return nil, fmt.Errorf("From: %w", err)
	}
	if len(from) == 0 {
		from = []*mail.Address{{Name: acct.DisplayName, Address: acct.Email}}
		h.SetAddressList("From", from)
	}
	p := &prepared{from: acct.Email}
	if config.IsOwn(own, from[0].Address) {
		p.from = from[0].Address
	}

	seen := map[string]bool{}
	var summary []string
	for _, key := range []string{"To", "Cc", "Bcc"} {
		list, err := h.AddressList(key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		for _, a := range list {
			k := strings.ToLower(a.Address)
			if seen[k] {
				continue
			}
			seen[k] = true
			p.rcpts = append(p.rcpts, a.Address)
			if key != "Bcc" {
				name := a.Name
				if name == "" {
					name = a.Address
				}
				summary = append(summary, name)
			}
		}
	}
	if len(p.rcpts) == 0 {
		return nil, errors.New("the message has no recipients")
	}
	p.to = strings.Join(summary, ", ")
	if p.to == "" {
		p.to = "Bcc recipients"
	}

	if !h.Has("Date") {
		h.SetDate(now)
	}
	if id, _ := h.MessageID(); id == "" {
		domain := "foonmail.invalid"
		if at := strings.LastIndex(p.from, "@"); at >= 0 {
			domain = p.from[at+1:]
		}
		h.SetMessageID(randomHex(16) + "@" + domain)
	}
	p.messageID, _ = h.MessageID()
	p.subject, _ = h.Subject()

	p.stored, err = render(h.Header.Header, body)
	if err != nil {
		return nil, err
	}
	h.Del("Bcc")
	p.wire, err = render(h.Header.Header, body)
	return p, err
}

func render(h textproto.Header, body []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := textproto.WriteHeader(&buf, h); err != nil {
		return nil, err
	}
	buf.Write(crlf(body))
	return buf.Bytes(), nil
}

// crlf turns bare LF line endings into CRLF, as SMTP and IMAP expect.
func crlf(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

func readAll(br *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(br)
	return buf.Bytes(), err
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
