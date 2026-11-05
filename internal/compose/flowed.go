package compose

import (
	"strings"
	"unicode/utf8"
)

// flowedWidth is where paragraphs wrap, as RFC 3676 recommends.
const flowedWidth = 72

// encodeFlowed encodes text as format=flowed (RFC 3676, DelSp=no): every
// line typed is wrapped at flowedWidth characters, the breaks it adds are
// soft (the line ends in a space), and the line breaks typed stay hard.
// Readers that know format=flowed re-flow paragraphs to their width;
// others see ordinary lines of 72 characters.
//
// Quoted lines (starting with ">") keep their quote depth on every
// wrapped line. Trailing spaces typed are dropped, since they would mean
// "continued"; the signature separator "-- " is kept as it is. Lines that
// start with a space, "From " or (unquoted) ">" are space-stuffed.
func encodeFlowed(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line == "-- " {
			out = append(out, line) // signature separator: never flowed
			continue
		}
		depth := 0
		for depth < len(line) && line[depth] == '>' {
			depth++
		}
		content := line[depth:]
		if depth > 0 {
			content = strings.TrimPrefix(content, " ") // the quote's own space
		}
		content = strings.TrimRight(content, " ")
		prefix := strings.Repeat(">", depth)

		for _, piece := range wrapFlowed(content, flowedWidth-len(prefix)-1) {
			out = append(out, prefix+stuff(piece, depth > 0))
		}
	}
	return strings.Join(out, "\n")
}

// wrapFlowed splits a line at spaces into pieces of at most width
// characters where it can (a word longer than that stays whole). Every
// piece but the last keeps the space it was split at, which makes it a
// soft break.
func wrapFlowed(s string, width int) []string {
	width = max(width, 20)
	var pieces []string
	for utf8.RuneCountInString(s) > width {
		cut := -1
		n := 0
		for i, r := range s {
			if n > width {
				break
			}
			if r == ' ' && i > 0 {
				cut = i
			}
			n++
		}
		if cut < 0 {
			// No space within the width: break after the long word instead.
			next := strings.IndexByte(s, ' ')
			if next < 0 {
				break
			}
			cut = next
		}
		pieces = append(pieces, s[:cut+1])
		s = s[cut+1:]
	}
	return append(pieces, s)
}

// stuff adds the leading space RFC 3676 asks for on lines a reader would
// otherwise misread: starting with a space (it would be taken as
// stuffing), "From " (mangled by some mail systems) or, unquoted, ">".
// Quoted lines always get the conventional space after the ">".
func stuff(s string, quoted bool) string {
	if quoted {
		if s == "" {
			return ""
		}
		return " " + s
	}
	if strings.HasPrefix(s, " ") || strings.HasPrefix(s, "From ") || strings.HasPrefix(s, ">") {
		return " " + s
	}
	return s
}
