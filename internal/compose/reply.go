package compose

import (
	"encoding/base64"
	"fmt"
	"foonmail/internal/hdr"
	"regexp"
	"slices"
	"strings"

	"foonmail/internal/config"
	"foonmail/internal/render"
)

var b64 = base64.StdEncoding

func base64Len(n int) int { return b64.EncodedLen(n) }

// New starts an empty message from an account, with its signature.
func New(acct *config.Account) *Draft {
	d := &Draft{From: identity(acct, "")}
	if s := signature(acct); s != "" {
		d.Body = "\n\n" + s
	}
	return d
}

// signature is the account's signature under its "-- " separator, ending
// in a newline, or "" if it has none.
func signature(acct *config.Account) string {
	s := strings.Trim(strings.ReplaceAll(acct.Signature, "\r\n", "\n"), "\n")
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "-- \n" + s + "\n"
}

// signed puts the account's signature between the place to write and what
// follows (the quote or the forwarded message), with a blank line after it.
func signed(acct *config.Account) string {
	if s := signature(acct); s != "" {
		return "\n\n" + s + "\n"
	}
	return "\n\n"
}

// ReplyMode says who a reply goes to.
type ReplyMode int

const (
	ReplySender ReplyMode = iota // the sender (or Reply-To)
	ReplyAll                     // the sender and every other recipient except you
)

// Reply starts a reply to a rendered message. The quoted text is the
// original's shown text, under an attribution line, with the cursor's
// place (an empty line) above it.
func Reply(acct *config.Account, orig *render.Message, mode ReplyMode) *Draft {
	own := acct.Addresses()
	isOwn := func(a render.Address) bool { return config.IsOwn(own, a.Addr) }

	d := &Draft{From: identity(acct, ownRecipient(orig, own))}
	fromMe := len(orig.From) > 0 && isOwn(orig.From[0])

	var to, cc []render.Address
	switch {
	case fromMe:
		// Replying to your own message (from Sent) means writing to its
		// recipients again.
		to = orig.To
		if mode == ReplyAll {
			cc = orig.Cc
		}
	default:
		to = orig.ReplyTo
		if len(to) == 0 {
			to = orig.From
		}
		if mode == ReplyAll {
			cc = append(slices.Clone(orig.To), orig.Cc...)
		}
	}
	seen := map[string]bool{}
	keep := func(list []render.Address) []render.Address {
		var out []render.Address
		for _, a := range list {
			k := strings.ToLower(a.Addr)
			if k == "" || seen[k] || (isOwn(a) && !fromMe) {
				continue
			}
			seen[k] = true
			out = append(out, a)
		}
		return out
	}
	d.To = addressString(keep(to))
	d.Cc = addressString(keep(cc))

	d.Subject = prefixed("Re: ", orig.Subject)
	d.InReplyTo = orig.MessageID
	d.References = references(orig)
	d.Body = signed(acct) + attribution(orig) + "\n" + quote(orig.Text) + "\n"
	return d
}

// Forward starts a forward of a rendered message, with the original's
// attachments. raw is the original message, for the attachments' content.
func Forward(acct *config.Account, orig *render.Message, raw []byte) (*Draft, error) {
	d := &Draft{From: identity(acct, ownRecipient(orig, acct.Addresses()))}
	d.Subject = prefixed("Fwd: ", orig.Subject)
	var b strings.Builder
	b.WriteString(signed(acct) + "---------- Forwarded message ----------\n")
	fmt.Fprintf(&b, "From: %s\n", addressString(orig.From))
	if !orig.Date.IsZero() {
		fmt.Fprintf(&b, "Date: %s\n", formatDate(orig.Date))
	}
	fmt.Fprintf(&b, "Subject: %s\n", orig.Subject)
	if len(orig.To) > 0 {
		fmt.Fprintf(&b, "To: %s\n", addressString(orig.To))
	}
	if len(orig.Cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", addressString(orig.Cc))
	}
	b.WriteString("\n" + orig.Text + "\n")
	d.Body = b.String()
	for _, a := range orig.Attachments {
		if a.Inline && strings.HasPrefix(a.ContentType, "image/") {
			continue // belongs to the HTML, which isn't forwarded
		}
		data, err := render.AttachmentData(raw, a.Index)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", a.Filename, err)
		}
		d.Attachments = append(d.Attachments, Attachment{
			Name: render.SafeFilename(a.Filename), ContentType: a.ContentType, Data: data,
		})
	}
	return d, nil
}

// identity is the From line: the address the original was sent to if it is
// one of yours (an alias), otherwise the account's address.
func identity(acct *config.Account, addr string) string {
	if addr == "" {
		addr = acct.Email
	}
	if acct.DisplayName == "" {
		return addr
	}
	return formatAddress(render.Address{Name: acct.DisplayName, Addr: addr})
}

// ownRecipient returns the first of your addresses the message was sent to.
func ownRecipient(m *render.Message, own []string) string {
	for _, a := range append(slices.Clone(m.To), m.Cc...) {
		if config.IsOwn(own, a.Addr) {
			return a.Addr
		}
	}
	return ""
}

func references(m *render.Message) []string {
	refs := slices.Clone(m.References)
	if m.MessageID != "" && !slices.Contains(refs, m.MessageID) {
		refs = append(refs, m.MessageID)
	}
	return refs
}

var subjectPrefix = regexp.MustCompile(`(?i)^\s*(re|fwd?|vs|sv|aw|wg|vl|tr)(\[\d+\])?\s*:\s*`)

// prefixed adds Re: or Fwd: unless the subject already starts with a reply
// or forward prefix of that kind.
func prefixed(prefix, subject string) string {
	s := strings.TrimSpace(subject)
	if m := subjectPrefix.FindStringSubmatch(s); m != nil {
		kind := strings.ToLower(m[1])
		isReply := kind == "re" || kind == "vs" || kind == "sv" || kind == "aw"
		if (prefix == "Re: " && isReply) || (prefix == "Fwd: " && !isReply) {
			return s
		}
	}
	return prefix + s
}

func attribution(m *render.Message) string {
	who := "someone"
	if len(m.From) > 0 {
		who = m.From[0].String()
	}
	if m.Date.IsZero() {
		return who + " wrote:"
	}
	return "On " + formatDate(m.Date) + ", " + who + " wrote:"
}

// quote prefixes every line with ">", keeping existing quote levels
// compact (">>" rather than "> >").
func quote(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		switch {
		case l == "":
			lines[i] = ">"
		case strings.HasPrefix(l, ">"):
			lines[i] = ">" + l
		default:
			lines[i] = "> " + l
		}
	}
	return strings.Join(lines, "\n")
}

func addressString(as []render.Address) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = formatAddress(a)
	}
	return strings.Join(s, ", ")
}

// formatAddress writes an address for the editable To/Cc/From fields:
// readable (UTF-8 kept as is) but quoted when the name has characters that
// would otherwise split or confuse an address list.
func formatAddress(a render.Address) string {
	if a.Name == "" {
		return a.Addr
	}
	name := a.Name
	if strings.ContainsAny(name, ",;:<>@()[]\".\\") {
		name = "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(name) + "\""
	}
	return name + " <" + a.Addr + ">"
}

// FromMessage reads a stored draft (or any message) back into an editable
// draft: addresses, subject, text, threading headers and attachments.
func FromMessage(raw []byte) (*Draft, error) {
	m, err := render.Render(raw, render.Options{})
	if err != nil {
		return nil, err
	}
	d := &Draft{
		Subject:    m.Subject,
		Body:       m.Text,
		InReplyTo:  m.InReplyTo,
		References: m.References,
	}
	if len(m.From) > 0 {
		d.From = formatAddress(m.From[0])
	}
	d.To, d.Cc, d.Bcc = addressString(m.To), addressString(m.Cc), addressString(m.Bcc)
	if strings.Contains(d.To, "undisclosed-recipients") {
		d.To = ""
	}
	// Fields saved before they parsed come back as they were typed.
	for key, field := range map[string]*string{"To": &d.To, "Cc": &d.Cc, "Bcc": &d.Bcc} {
		if v := draftHeader(raw, "X-Foonmail-Draft-"+key); v != "" {
			*field = v
		}
	}
	for _, a := range m.Attachments {
		if a.Inline && strings.HasPrefix(a.ContentType, "image/") {
			continue
		}
		data, err := render.AttachmentData(raw, a.Index)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", a.Filename, err)
		}
		d.Attachments = append(d.Attachments, Attachment{
			Name: render.SafeFilename(a.Filename), ContentType: a.ContentType, Data: data,
		})
	}
	return d, nil
}

// draftHeader reads one of BuildDraft's X- headers.
func draftHeader(raw []byte, key string) string {
	head, _, _ := strings.Cut(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n\n")
	prefix := strings.ToLower(key) + ":"
	for _, line := range strings.Split(head, "\n") {
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			v := strings.TrimSpace(line[len(prefix):])
			return hdr.Decode(v)
		}
	}
	return ""
}
