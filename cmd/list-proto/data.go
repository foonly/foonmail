package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// message is one conversation row in the fake INBOX.
type message struct {
	id      int
	from    string
	subject string
	date    time.Time
	count   int // messages in the conversation
	unread  bool
	deleted bool
	search  string // lowercased from + subject, for filtering
}

var senders = []string{
	"Bob Example", "Örjan Ääkkönen", "Liisa Mäkelä", "Pekka Virtanen", "Anna Söderström",
	"GitHub", "Hetzner Online", "Kela", "Jukka Hyvönen", "Sari Lehtinen",
	"Postmaster", "Matti Meikäläinen", "Emma Johansson", "Dovecot mailing list",
	"Tilitoimisto Kärkkäinen Oy", "Noora Heikkilä", "Ville Räsänen", "Calendar",
	"A sender with a rather long display name that will need truncating", "Tuomas Ström",
}

var subjects = []string{
	"Kahvitauko torstaina?",
	"Invoice %d for October",
	"[foonmail] Pull request #%d: Collapse trailing quotes",
	"Server maintenance on Saturday",
	"Lounas huomenna klo 11.30",
	"Re: Re: Fwd: Questions about the contract draft",
	"Your order %d has shipped",
	"Viikkopalaveri siirtyy",
	"Dovecot 2.3 QRESYNC question",
	"Weekend plans",
	"Kuitti: ostos %d",
	"A very long subject line that keeps going well past the width of the window so that the list has to truncate it with an ellipsis",
	"Security alert for your account",
	"Pöytäkirja hallituksen kokouksesta",
	"Quarterly report draft, comments welcome",
	"Muistutus: hammaslääkäri tiistaina",
	"Build failed on main (%d)",
	"Photos from the trip",
	"Sieve rule not matching mailing lists",
	"Tervetuloa!",
}

// generate makes n conversations, newest first, deterministically.
func generate(n int, now time.Time) []message {
	r := rand.New(rand.NewPCG(1, 2))
	msgs := make([]message, n)
	t := now
	for i := range msgs {
		t = t.Add(-time.Duration(3+r.IntN(600)) * time.Minute)
		subj := subjects[r.IntN(len(subjects))]
		if strings.Contains(subj, "%d") {
			subj = fmt.Sprintf(subj, 1000+r.IntN(9000))
		}
		count := 1
		if r.IntN(4) == 0 {
			count = 2 + r.IntN(11)
		}
		from := senders[r.IntN(len(senders))]
		msgs[i] = message{
			id:      i,
			from:    from,
			subject: subj,
			date:    t,
			count:   count,
			unread:  i < 40 && r.IntN(2) == 0 || r.IntN(25) == 0,
			search:  strings.ToLower(from + " " + subj),
		}
	}
	return msgs
}

// fakeBody returns a plain-text body for viewing, with a quoted tail.
func fakeBody(m message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hei,\n\ntämä on viesti numero %d keskustelusta \"%s\".\n\n", m.id, m.subject)
	b.WriteString("Here is a long line to test wrapping and selection in the viewer: ääkköset äöå ÄÖÅ, € and an em dash — " +
		"followed by enough words that it must wrap at least once in a normal window width.\n\n")
	for p := 1; p <= 6; p++ {
		fmt.Fprintf(&b, "Paragraph %d. Lorem ipsum dolor sit amet, consectetur adipiscing elit. "+
			"Pellentesque habitant morbi tristique senectus et netus et malesuada fames ac turpis egestas.\n\n", p)
	}
	b.WriteString("Terveisin,\n" + m.from + "\n\n")
	fmt.Fprintf(&b, "On %s, Someone <someone@example.com> wrote:\n", m.date.Add(-3*time.Hour).Format("Mon, 2 Jan 2006 at 15:04"))
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, "> Quoted line %d of the earlier message.\n", i)
	}
	return b.String()
}

// shortDate formats a date for the list: time today, day and month this
// year, full date otherwise.
func shortDate(t, now time.Time) string {
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case y1 == y2:
		return t.Format("2 Jan")
	default:
		return t.Format("2006-01-02")
	}
}
