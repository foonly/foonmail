// Command render-sample fetches recent messages from a folder and renders
// them, to check the renderer against real mail.
//
// For each message it writes NNN.eml (the raw message) and NNN.txt (what
// the reader would show) into a private directory. On stdout it prints only
// statistics, no subjects or senders. The folder is opened read-only and
// bodies are fetched with BODY.PEEK, so nothing is marked as read.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/zalando/go-keyring"

	"foonmail/internal/config"
	"foonmail/internal/render"
)

func main() {
	defaultPath, _ := config.Path()
	configPath := flag.String("config", defaultPath, "configuration file")
	account := flag.String("account", "", "account name (default: the first)")
	folder := flag.String("folder", "INBOX", "folder to sample")
	n := flag.Int("n", 30, "number of most recent messages")
	out := flag.String("out", filepath.Join(os.TempDir(), "foonmail-render-"+time.Now().Format("150405")), "output directory")
	flag.Parse()
	if err := run(*configPath, *account, *folder, *n, *out); err != nil {
		fmt.Fprintln(os.Stderr, "render-sample:", err)
		os.Exit(1)
	}
}

func run(configPath, accountName, folder string, n int, out string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	acct := &cfg.Accounts[0]
	if accountName != "" {
		if acct, err = cfg.Account(accountName); err != nil {
			return err
		}
	}
	service, user := acct.IMAP.SecretService()
	pass, err := keyring.Get(service, user)
	if err != nil {
		return fmt.Errorf("password: %w", err)
	}

	var c *imapclient.Client
	if acct.IMAP.Security == config.SecurityStartTLS {
		c, err = imapclient.DialStartTLS(acct.IMAP.Address(), nil)
	} else {
		c, err = imapclient.DialTLS(acct.IMAP.Address(), nil)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login(user, pass).Wait(); err != nil {
		return err
	}
	sel, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return err
	}
	if sel.NumMessages == 0 {
		return errors.New("folder is empty")
	}
	start := uint32(1)
	if sel.NumMessages > uint32(n) {
		start = sel.NumMessages - uint32(n) + 1
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(imap.SeqSet{{Start: start, Stop: sel.NumMessages}},
		&imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	fmt.Printf("%-4s %-7s %-6s %6s %5s %-12s %5s %s\n", "#", "uid", "source", "chars", "links", "quotes", "atts", "notes")
	for i, m := range msgs {
		rawMsg := m.FindBodySection(section)
		base := filepath.Join(out, fmt.Sprintf("%03d", i+1))
		if err := os.WriteFile(base+".eml", rawMsg, 0o600); err != nil {
			return err
		}
		r, err := renderSafely(rawMsg)
		if err != nil {
			fmt.Printf("%-4d %-7d ERROR: %v\n", i+1, m.UID, err)
			continue
		}
		if err := os.WriteFile(base+".txt", []byte(describe(r)), 0o600); err != nil {
			return err
		}
		quotes := fmt.Sprint(len(r.Quotes))
		for _, q := range r.Quotes {
			if q.Trailing {
				quotes += " trailing"
				break
			}
		}
		var notes []string
		if r.HasPlain && r.HasHTML {
			notes = append(notes, "alt")
		}
		if r.Source == render.SourceNone {
			notes = append(notes, "NO BODY")
		}
		if len(r.Warnings) > 0 {
			notes = append(notes, fmt.Sprintf("%d warnings", len(r.Warnings)))
		}
		fmt.Printf("%-4d %-7d %-6s %6d %5d %-12s %5d %s\n", i+1, m.UID, r.Source, len(r.Text),
			len(r.Links), quotes, len(r.Attachments), strings.Join(notes, ", "))
	}
	fmt.Printf("\nWrote %d messages to %s (NNN.eml raw, NNN.txt rendered)\n", len(msgs), out)
	return nil
}

func renderSafely(raw []byte) (m *render.Message, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return render.Render(raw, render.Options{})
}

// describe lays out a rendered message the way the reader would, with
// collapsed trailing quotes marked and the link list at the end.
func describe(m *render.Message) string {
	var b strings.Builder
	addrs := func(as []render.Address) string {
		s := make([]string, len(as))
		for i, a := range as {
			s[i] = a.String()
		}
		return strings.Join(s, ", ")
	}
	fmt.Fprintf(&b, "From: %s\nTo: %s\n", addrs(m.From), addrs(m.To))
	if len(m.Cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\n", addrs(m.Cc))
	}
	fmt.Fprintf(&b, "Date: %s\nSubject: %s\nSource: %s (plain part: %v, HTML part: %v)\n",
		m.Date.Format(time.RFC1123Z), m.Subject, m.Source, m.HasPlain, m.HasHTML)
	for _, w := range m.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", w)
	}
	b.WriteString(strings.Repeat("─", 72) + "\n")

	text := m.Text
	for i := len(m.Quotes) - 1; i >= 0; i-- {
		q := m.Quotes[i]
		if !q.Trailing {
			continue
		}
		who := q.Who
		if who == "" {
			who = "someone"
		}
		text = text[:q.Start] + fmt.Sprintf("[… quoted text from %s, %d lines]", who, q.Lines) + text[q.End:]
	}
	b.WriteString(text)
	b.WriteString("\n")

	if len(m.Links) > 0 {
		b.WriteString("\n" + strings.Repeat("─", 72) + "\n")
		for _, l := range m.Links {
			fmt.Fprintf(&b, "[%d] %s\n", l.N, render.ShortURL(l.URL, 70))
		}
	}
	if len(m.Attachments) > 0 {
		b.WriteString("\nAttachments:\n")
		for _, a := range m.Attachments {
			inline := ""
			if a.Inline {
				inline = ", inline"
			}
			fmt.Fprintf(&b, "  %s (%s, %d bytes%s)\n", render.SafeFilename(a.Filename), a.ContentType, a.Size, inline)
		}
	}
	return b.String()
}
