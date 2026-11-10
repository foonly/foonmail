package main

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// searchProbe times what a server search across folders would cost: for
// each folder but Trash and Junk, a header search (each word in From, To or
// Subject) and a full-text search (each word anywhere, body included).
// Folders are opened read-only.
func (p *prober) searchProbe(words []string) error {
	opts := &imapclient.Options{
		Dialer:      &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		DebugWriter: p.debug,
	}
	dial := p.dial
	if dial == nil {
		dial = imapclient.DialTLS
	}
	c, err := dial(p.addr, opts)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login(p.user, p.pass).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	list, err := c.List("", "*", &imap.ListOptions{ReturnSpecialUse: true, ReturnStatus: &imap.StatusOptions{NumMessages: true}}).Collect()
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}

	header := imap.SearchCriteria{}
	for _, w := range words {
		header.And(&imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{
			{Header: []imap.SearchCriteriaHeaderField{{Key: "From", Value: w}}},
			{Or: [][2]imap.SearchCriteria{{
				{Header: []imap.SearchCriteriaHeaderField{{Key: "To", Value: w}}},
				{Header: []imap.SearchCriteriaHeaderField{{Key: "Subject", Value: w}}},
			}}},
		}}})
	}
	text := imap.SearchCriteria{Text: words}

	tw := tabwriter.NewWriter(p.out.Writer(), 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "FOLDER\tMESSAGES\tHEADER HITS\tHEADER TIME\tTEXT HITS\tTEXT TIME\t")
	var total, hHits, tHits int
	var hTime, tTime time.Duration
	for _, mb := range list {
		skip := slices.ContainsFunc(mb.Attrs, func(a imap.MailboxAttr) bool {
			return a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent ||
				a == imap.MailboxAttrTrash || a == imap.MailboxAttrJunk
		})
		if skip {
			continue
		}
		n := 0
		if mb.Status != nil && mb.Status.NumMessages != nil {
			n = int(*mb.Status.NumMessages)
		}
		if n == 0 {
			continue
		}
		if _, err := c.Select(mb.Mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			fmt.Fprintf(tw, "%s\t%d\tselect failed: %v\t\t\t\t\n", mb.Mailbox, n, err)
			continue
		}
		search := func(crit *imap.SearchCriteria) (int, time.Duration, error) {
			start := time.Now()
			data, err := c.UIDSearch(crit, nil).Wait()
			if err != nil {
				return 0, time.Since(start), err
			}
			return len(data.AllUIDs()), time.Since(start), nil
		}
		hn, ht, err := search(&header)
		if err != nil {
			return fmt.Errorf("%s: header search: %w", mb.Mailbox, err)
		}
		tn, tt, err := search(&text)
		if err != nil {
			return fmt.Errorf("%s: text search: %w", mb.Mailbox, err)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%d\t%s\t\n", mb.Mailbox, n, hn, ms(ht), tn, ms(tt))
		total += n
		hHits += hn
		tHits += tn
		hTime += ht
		tTime += tt
	}
	fmt.Fprintf(tw, "all\t%d\t%d\t%s\t%d\t%s\t\n", total, hHits, ms(hTime), tHits, ms(tTime))
	fmt.Fprintf(tw, "searched for\t%q\t\t\t\t\t\n", strings.Join(words, " "))
	return tw.Flush()
}

func ms(d time.Duration) string { return d.Round(time.Millisecond).String() }
