// Package hdr decodes RFC 2047 encoded words ("=?windows-1252?Q?...?=")
// in mail headers, in any charset the mail world uses.
package hdr

import (
	"mime"
	"strings"

	"github.com/emersion/go-message/charset"
)

// Decoder knows the legacy charsets (ISO-8859-*, Windows-125x, KOI8…) as
// well as UTF-8; the standard library's own knows only UTF-8, ISO-8859-1
// and US-ASCII, and leaves the rest encoded.
var Decoder = &mime.WordDecoder{CharsetReader: charset.Reader}

// Decode decodes the encoded words in a header value. A value that can't
// be decoded is returned as it is.
func Decode(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	d, err := Decoder.DecodeHeader(s)
	if err != nil {
		return s
	}
	return d
}
