package render

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// converted is HTML turned into text.
type converted struct {
	text  string
	links []Link
}

// Elements whose content is never shown.
var skipped = map[atom.Atom]bool{
	atom.Head: true, atom.Style: true, atom.Script: true, atom.Title: true, atom.Noscript: true,
	atom.Template: true, atom.Svg: true, atom.Math: true, atom.Object: true, atom.Iframe: true,
}

// Elements that start a paragraph (a blank line before and after).
var paragraphs = map[atom.Atom]bool{
	atom.P: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Blockquote: true, atom.Pre: true, atom.Ul: true, atom.Ol: true, atom.Table: true, atom.Hr: true,
	atom.Address: true, atom.Center: true, atom.Figure: true, atom.Dl: true,
}

// Elements that start a new line.
var lines = map[atom.Atom]bool{
	atom.Div: true, atom.Tr: true, atom.Li: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Nav: true, atom.Aside: true, atom.Main: true,
	atom.Dd: true, atom.Dt: true, atom.Caption: true, atom.Fieldset: true, atom.Form: true,
	atom.Tbody: true, atom.Thead: true, atom.Tfoot: true,
}

// hiddenStyle matches inline styles that hide content. Newsletters hide a
// "preheader" (the preview line) this way, often padded with invisible
// characters.
var hiddenStyle = regexp.MustCompile(`(?i)display\s*:\s*none|visibility\s*:\s*hidden|mso-hide\s*:\s*all|` +
	`max-height\s*:\s*0(px)?\s*(;|!|$)|opacity\s*:\s*0(\.0+)?\s*(;|!|$)|font-size\s*:\s*0(px)?\s*(;|!|$)`)

// invisible are characters with no width that newsletters use as padding.
var invisible = strings.NewReplacer(
	"\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "", "\u034f", "", "\u00ad", "",
)

type outLine struct {
	depth int // quote depth
	text  string
}

type listState struct {
	ordered bool
	n       int
}

type htmlConv struct {
	lines    []outLine
	cur      strings.Builder
	curDepth int
	breakReq int  // 1: new line, 2: blank line, before the next text
	space    bool // a space is due before the next word
	written  int  // characters written so far, to tell whether a link had text

	depth       int // current quote depth
	pre         int
	lists       []listState
	outlookRest bool // everything from here on is an Outlook-quoted original

	links       []Link
	linkNum     map[string]int
	linkOffset  int
	linkPending bool // a link's span starts at the next word
}

func htmlToText(src string, linkOffset int) converted {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return converted{text: tidy(src)}
	}
	c := &htmlConv{linkNum: map[string]int{}, linkOffset: linkOffset}
	c.node(doc)
	c.endLine()
	return converted{text: c.render(), links: c.links}
}

func (c *htmlConv) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		c.text(n.Data)
		return
	case html.ElementNode:
	case html.DocumentNode:
		c.children(n)
		return
	default:
		return
	}

	if skipped[n.DataAtom] || isHidden(n) {
		return
	}

	// Outlook puts the original message after a marker element, unquoted.
	if id := attr(n, "id"); !c.outlookRest && (id == "divRplyFwdMsg" || id == "appendonsend") {
		c.outlookRest = true
		c.lineBreak(2)
		c.depth++
	}

	switch n.DataAtom {
	case atom.Br:
		// A <br> on top of a pending line break (another <br>, or the end of a
		// block, as in Gmail's <div><br></div> blank lines) makes a blank line.
		if c.breakReq >= 1 {
			c.lineBreak(2)
		} else {
			c.lineBreak(1)
		}
		return
	case atom.Img:
		if alt := strings.TrimSpace(attr(n, "alt")); alt != "" {
			c.text("[" + alt + "]")
		}
		return
	case atom.A:
		c.link(n)
		return
	case atom.Td, atom.Th:
		c.space = true
		c.children(n)
		c.space = true
		return
	case atom.Blockquote:
		c.lineBreak(2)
		c.depth++
		c.children(n)
		c.depth--
		c.lineBreak(2)
		return
	case atom.Pre:
		c.lineBreak(2)
		c.pre++
		c.children(n)
		c.pre--
		c.lineBreak(2)
		return
	case atom.Ul, atom.Ol:
		gap := 2
		if len(c.lists) > 0 {
			gap = 1 // a nested list continues its parent
		}
		c.lineBreak(gap)
		c.lists = append(c.lists, listState{ordered: n.DataAtom == atom.Ol})
		c.children(n)
		c.lists = c.lists[:len(c.lists)-1]
		c.lineBreak(gap)
		return
	case atom.Li:
		c.lineBreak(1)
		marker := "• "
		if len(c.lists) > 0 {
			l := &c.lists[len(c.lists)-1]
			l.n++
			if l.ordered {
				marker = strconv.Itoa(l.n) + ". "
			}
		}
		indent := strings.Repeat("  ", max(0, len(c.lists)-1))
		c.raw(indent + marker)
		c.children(n)
		c.lineBreak(1)
		return
	}

	switch {
	case paragraphs[n.DataAtom]:
		c.lineBreak(2)
		c.children(n)
		c.lineBreak(2)
	case lines[n.DataAtom]:
		c.lineBreak(1)
		c.children(n)
		c.lineBreak(1)
	default:
		c.children(n)
	}
}

func (c *htmlConv) children(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.node(ch)
	}
}

// link writes the link's content followed by "[N]" for a followable URL.
func (c *htmlConv) link(n *html.Node) {
	before := c.written
	c.linkPending = true // the next word written starts the link's span
	c.children(n)
	started := !c.linkPending
	c.linkPending = false
	cancel := func() {
		if started {
			c.cur.WriteString(markCancel)
		}
	}
	href := strings.TrimSpace(attr(n, "href"))
	u, err := url.Parse(href)
	if err != nil || c.written == before {
		cancel()
		return
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "ftp":
	default:
		cancel()
		return // javascript:, #anchors, relative links
	}
	num, ok := c.linkNum[href]
	if !ok {
		num = c.linkOffset + len(c.links) + 1
		c.linkNum[href] = num
		c.links = append(c.links, Link{N: num, URL: href})
	}
	c.space = true
	c.text("[" + strconv.Itoa(num) + "]")
	if started {
		c.cur.WriteString(markEnd)
	}
}

// text writes a text node, collapsing whitespace outside <pre>.
func (c *htmlConv) text(s string) {
	s = invisible.Replace(s)
	s = stripMarks(s)
	if c.pre > 0 {
		for i, part := range strings.Split(s, "\n") {
			if i > 0 {
				c.lineBreak(1)
				c.forceLine()
			}
			c.raw(part)
		}
		return
	}
	if s == "" {
		return
	}
	if isSpace(rune(s[0])) {
		c.space = true
	}
	words := strings.Fields(s)
	for i, w := range words {
		if i > 0 {
			c.space = true
		}
		c.word(w)
	}
	if len(words) > 0 && isSpace(rune(s[len(s)-1])) {
		c.space = true
	}
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u00a0' }

func (c *htmlConv) word(w string) {
	c.startText()
	if c.space && c.cur.Len() > 0 {
		c.cur.WriteByte(' ')
	}
	c.space = false
	if c.linkPending {
		c.cur.WriteString(markStart)
		c.linkPending = false
	}
	c.cur.WriteString(w)
	c.written += len(w)
}

// raw writes text as is (list markers, <pre> content).
func (c *htmlConv) raw(s string) {
	c.startText()
	c.space = false
	c.cur.WriteString(s)
	c.written += len(s)
}

// startText applies a pending line break before text is written.
func (c *htmlConv) startText() {
	if c.breakReq > 0 {
		if c.cur.Len() > 0 || len(c.lines) > 0 {
			c.endLine()
			if c.breakReq == 2 && len(c.lines) > 0 {
				c.lines = append(c.lines, outLine{depth: min(c.depth, c.lines[len(c.lines)-1].depth)})
			}
		}
		c.breakReq = 0
		c.space = false
	}
	if c.cur.Len() == 0 {
		c.curDepth = c.depth
	}
}

func (c *htmlConv) forceLine() {
	c.endLine()
	c.breakReq = 0
	c.curDepth = c.depth
}

func (c *htmlConv) lineBreak(kind int) {
	c.breakReq = max(c.breakReq, kind)
}

func (c *htmlConv) endLine() {
	if c.cur.Len() > 0 {
		c.lines = append(c.lines, outLine{depth: c.curDepth, text: c.cur.String()})
		c.cur.Reset()
	}
}

func (c *htmlConv) render() string {
	var b strings.Builder
	for _, l := range c.lines {
		if l.depth > 0 {
			b.WriteString(strings.Repeat(">", l.depth))
			if l.text != "" {
				b.WriteByte(' ')
			}
		}
		b.WriteString(l.text)
		b.WriteByte('\n')
	}
	return tidy(b.String())
}

func isHidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "hidden":
			return true
		case "aria-hidden":
			// Decorative only; hiding it would drop real text in some mail.
		case "style":
			if hiddenStyle.MatchString(a.Val) {
				return true
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
