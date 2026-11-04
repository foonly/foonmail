package main

import (
	"bytes"
	"io"
	"regexp"
	"sync"
)

// redactWriter is the protocol log for -debug. It works line by line and
// hides credentials: the arguments of LOGIN and AUTHENTICATE commands, and
// the line after one that ends in a literal ({n} or {n+}), which is how a
// password with special characters is sent.
type redactWriter struct {
	mu         sync.Mutex
	w          io.Writer
	buf        []byte
	redactNext bool
}

var (
	authLine   = regexp.MustCompile(`(?i)^(\S+ (?:UID )?(?:LOGIN|AUTHENTICATE))( .*)$`)
	literalEnd = regexp.MustCompile(`\{\d+\+?\}\r?$`)
)

func newRedactWriter(w io.Writer) *redactWriter { return &redactWriter{w: w} }

func (r *redactWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			break
		}
		line := string(r.buf[:i])
		r.buf = r.buf[i+1:]
		if _, err := io.WriteString(r.w, r.redact(line)+"\n"); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

func (r *redactWriter) redact(line string) string {
	if r.redactNext {
		r.redactNext = literalEnd.MatchString(line)
		return "<redacted>"
	}
	if m := authLine.FindStringSubmatch(line); m != nil {
		r.redactNext = literalEnd.MatchString(line)
		return m[1] + " <redacted>"
	}
	return line
}
