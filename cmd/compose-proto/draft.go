package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// draft is the editable state of a message being composed.
type draft struct {
	To, Cc, Subject string
	Attach          []string // absolute paths
	Body            string
}

type editOutcome int

const (
	editApplied   editOutcome = iota // the editor changed the draft and it parsed cleanly
	editUnchanged                    // the editor exited without changing anything
	editCancelled                    // the user gave up after an error round
)

// problemPrefix marks the error comments foonmail adds to the header block.
const problemPrefix = "# foonmail: "

const draftHelp = "# Lines starting with # are ignored up to the first blank line.\n" +
	"# Attach files with lines like: Attach: ~/Documents/file.pdf\n"

// formatDraft renders a draft as an editable text file: a header block,
// a blank line, then the body.
func formatDraft(d draft) []byte {
	var b bytes.Buffer
	b.WriteString(draftHelp)
	fmt.Fprintf(&b, "To: %s\n", d.To)
	fmt.Fprintf(&b, "Cc: %s\n", d.Cc)
	fmt.Fprintf(&b, "Subject: %s\n", d.Subject)
	for _, p := range d.Attach {
		fmt.Fprintf(&b, "Attach: %s\n", p)
	}
	b.WriteString("\n")
	b.WriteString(d.Body)
	if !strings.HasSuffix(d.Body, "\n") {
		b.WriteString("\n")
	}
	return b.Bytes()
}

// parseDraft parses a file written by formatDraft and edited by the user.
// Problems are returned as human-readable strings; any problem means the
// draft must not be used.
func parseDraft(data []byte) (draft, []string) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	head, body, _ := strings.Cut(text, "\n\n")

	var d draft
	var problems []string
	seen := map[string]bool{}
	var last *string // the previous header's value, for folded lines

	for i, line := range strings.Split(head, "\n") {
		line = strings.TrimRight(line, " \t")
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case line[0] == ' ' || line[0] == '\t':
			if last == nil {
				problems = append(problems, fmt.Sprintf("line %d: continuation line without a header", i+1))
				continue
			}
			*last += " " + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			problems = append(problems, fmt.Sprintf("line %d is not a header (Name: value): %q", i+1, line))
			last = nil
			continue
		}
		value = strings.TrimSpace(value)
		last = nil
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "to":
			d.To, last = value, &d.To
		case "cc":
			d.Cc, last = value, &d.Cc
		case "subject":
			d.Subject, last = value, &d.Subject
		case "attach":
			if value == "" {
				continue
			}
			p, err := expandPath(value)
			if err != nil {
				problems = append(problems, err.Error())
				continue
			}
			if fi, err := os.Stat(p); err != nil {
				problems = append(problems, "attachment not found: "+value)
			} else if fi.IsDir() {
				problems = append(problems, "attachment is a directory: "+value)
			} else if !seen[p] {
				seen[p] = true
				d.Attach = append(d.Attach, p)
			}
		default:
			problems = append(problems, fmt.Sprintf("unknown header %q", strings.TrimSpace(name)))
		}
	}
	d.Body = strings.TrimSuffix(body, "\n")
	return d, problems
}

func expandPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("can't expand %s: %w", p, err)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("attachment path must be absolute or start with ~/: %s", p)
	}
	return filepath.Clean(p), nil
}

// annotate puts problem comments at the top of an edited draft, replacing
// any left over from an earlier round.
func annotate(edited []byte, problems []string) []byte {
	var b bytes.Buffer
	for _, p := range problems {
		b.WriteString(problemPrefix + p + "\n")
	}
	inHead := true
	for _, line := range strings.SplitAfter(string(edited), "\n") {
		if inHead && strings.TrimSpace(line) == "" {
			inHead = false
		}
		if inHead && strings.HasPrefix(line, problemPrefix) {
			continue
		}
		b.WriteString(line)
	}
	return b.Bytes()
}

// editExternally writes the draft to a temporary file, runs argv with the
// file's path appended, and parses the result. If the result has problems
// (such as a missing attachment) the editor is reopened with the problems
// listed at the top. Closing the editor without changes ends the loop.
func editExternally(argv []string, d draft) (draft, editOutcome, error) {
	f, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), "foonmail-draft-*.eml")
	if err != nil {
		return d, 0, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	content := formatDraft(d)
	for round := 0; ; round++ {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return d, 0, err
		}
		cmd := exec.Command(argv[0], append(argv[1:], path)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return d, 0, fmt.Errorf("%s: %w", argv[0], err)
		}
		edited, err := os.ReadFile(path)
		if err != nil {
			return d, 0, err
		}
		if bytes.Equal(edited, content) {
			if round == 0 {
				return d, editUnchanged, nil
			}
			return d, editCancelled, nil
		}
		nd, problems := parseDraft(edited)
		if len(problems) == 0 {
			return nd, editApplied, nil
		}
		content = annotate(edited, problems)
	}
}

func baseName(p string) string { return filepath.Base(p) }

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
