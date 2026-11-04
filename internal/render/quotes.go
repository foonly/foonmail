package render

import (
	"regexp"
	"strings"
)

// Quote is a quoted region of Message.Text.
type Quote struct {
	Start, End  int    // byte offsets into Text; Start includes the attribution line
	Attribution string // "On Tue, Bob <bob@example.com> wrote:", or "" if none was found
	Who         string // best guess at the quoted person's name, or ""
	Lines       int    // quoted lines, not counting the attribution
	// Trailing is true when nothing but blank lines follows the region:
	// the usual top-posted reply, safe to collapse. Quotes with answers
	// written between them are not trailing and must stay visible.
	Trailing bool
}

var (
	// "On …, Bob wrote:" and its translations, and the forms where the verb
	// comes before the name ("Am …, schrieb Bob:").
	attributionRe = regexp.MustCompile(`(?i)(\b(wrote|kirjoitti|skrev|schrieb|schreef|napisał|escribió|ha scritto)|a écrit)\s*:\s*$` +
		`|\b(skrev|schrieb)\s+.+:\s*$`)
	// Outlook's plain-text separator and its header block.
	outlookSep   = regexp.MustCompile(`^\s*(-{3,}\s*(Original Message|Alkuperäinen viesti|Ursprungligt meddelande|Ursprüngliche Nachricht)\s*-{3,}|_{10,})\s*$`)
	outlookFrom  = regexp.MustCompile(`^\*?(From|Lähettäjä|Från|Von|De)\*?:\s*(.+)$`)
	outlookDate  = regexp.MustCompile(`^\*?(Sent|Date|Lähetetty|Päivämäärä|Skickat|Datum|Gesendet|Envoyé)\*?:`)
	outlookSubj  = regexp.MustCompile(`^\*?(Subject|Aihe|Ämne|Betreff|Objet)\*?:`)
	wroteTail    = regexp.MustCompile(`(?i)\s*(\b(wrote|kirjoitti|skrev|schrieb|schreef|napisał|escribió|ha scritto)|a écrit)\s*:\s*$`)
	verbFirst    = regexp.MustCompile(`(?i)\b(skrev|schrieb)\s+(.+?)\s*:\s*$`)
	finnishTime  = regexp.MustCompile(`klo\s+\d{1,2}[.:]\d{2}\s+`)
	angleOrParen = regexp.MustCompile(`\s*(<[^>]*>|\([^)]*\)|\[mailto:[^\]]*\])`)
)

type line struct {
	start, end int // byte offsets of the line, without its newline
	text       string
}

func splitLines(s string) []line {
	var out []line
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			out = append(out, line{start: start, end: i, text: s[start:i]})
			start = i + 1
		}
	}
	return out
}

// findQuotes finds quoted regions: runs of ">" lines with the attribution
// line above them, and Outlook's unquoted "From: / Sent: / Subject:" block
// followed by the original message.
func findQuotes(text string) []Quote {
	ls := splitLines(text)
	var quotes []Quote
	for i := 0; i < len(ls); i++ {
		if isOutlookStart(ls, i) {
			quotes = append(quotes, outlookQuote(text, ls, i))
			break // everything after is the original message
		}
		if !strings.HasPrefix(ls[i].text, ">") {
			continue
		}
		// Extend over quoted lines, and blank lines between quoted lines.
		j := i
		for k := i; k < len(ls); k++ {
			if strings.HasPrefix(ls[k].text, ">") {
				j = k
			} else if strings.TrimSpace(ls[k].text) != "" {
				break
			}
		}
		q := Quote{Start: ls[i].start, End: ls[j].end, Lines: j - i + 1}
		if a, start := attribution(ls, i); start >= 0 {
			q.Attribution, q.Start = a, ls[start].start
			q.Who = whoFromAttribution(a)
		}
		q.Trailing = strings.TrimSpace(text[q.End:]) == ""
		quotes = append(quotes, q)
		i = j
	}
	return quotes
}

// attribution looks for the "… wrote:" line just above quoted line i,
// allowing one blank line between and an attribution wrapped over two lines.
func attribution(ls []line, i int) (string, int) {
	k := i - 1
	if k >= 0 && strings.TrimSpace(ls[k].text) == "" {
		k--
	}
	if k < 0 || strings.HasPrefix(ls[k].text, ">") {
		return "", -1
	}
	a := strings.TrimSpace(ls[k].text)
	if !attributionRe.MatchString(a) {
		return "", -1
	}
	// Gmail wraps long attributions: "On Tue, …, Bob Example <" / "bob@example.com> wrote:".
	if k > 0 && !startsAttribution(a) && strings.TrimSpace(ls[k-1].text) != "" && !strings.HasPrefix(ls[k-1].text, ">") {
		prev := strings.TrimSpace(ls[k-1].text)
		if startsAttribution(prev) {
			return prev + " " + a, k - 1
		}
	}
	return a, k
}

func startsAttribution(s string) bool {
	lower := strings.ToLower(s)
	for _, p := range []string{"on ", "le ", "am ", "den ", "el ", "il ", "op ",
		"ma ", "ti ", "ke ", "to ", "pe ", "la ", "su "} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func whoFromAttribution(a string) string {
	if m := verbFirst.FindStringSubmatch(a); m != nil {
		return cleanName(m[2])
	}
	s := wroteTail.ReplaceAllString(a, "")
	if loc := finnishTime.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	} else if i := strings.LastIndex(s, ", "); i >= 0 {
		s = s[i+2:]
	}
	return cleanName(s)
}

func cleanName(s string) string {
	s = angleOrParen.ReplaceAllString(s, "")
	s = strings.Trim(strings.TrimSpace(s), `"'`)
	if strings.Contains(s, "@") && !strings.Contains(s, " ") {
		return s // only an address
	}
	return s
}

func isOutlookStart(ls []line, i int) bool {
	t := strings.TrimSpace(ls[i].text)
	if outlookSep.MatchString(t) {
		return true
	}
	if !outlookFrom.MatchString(t) {
		return false
	}
	var date, subj bool
	for k := i + 1; k < len(ls) && k <= i+5; k++ {
		t := strings.TrimSpace(ls[k].text)
		date = date || outlookDate.MatchString(t)
		subj = subj || outlookSubj.MatchString(t)
	}
	return date && subj
}

func outlookQuote(text string, ls []line, i int) Quote {
	q := Quote{Start: ls[i].start, End: len(strings.TrimRight(text, "\n ")), Trailing: true}
	for k := i; k < len(ls) && k <= i+6; k++ {
		if m := outlookFrom.FindStringSubmatch(strings.TrimSpace(ls[k].text)); m != nil {
			q.Attribution = strings.TrimSpace(ls[k].text)
			q.Who = cleanName(m[2])
			break
		}
	}
	for _, l := range ls[i:] {
		if l.start < q.End {
			q.Lines++
		}
	}
	return q
}
