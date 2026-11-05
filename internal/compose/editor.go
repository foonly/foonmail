package compose

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EditOutcome is what happened in the external editor.
type EditOutcome int

const (
	EditApplied   EditOutcome = iota // the draft changed and parsed cleanly
	EditUnchanged                    // closed without changes
	EditCancelled                    // closed without changes after an error round
)

// problemPrefix marks the error comments added to the header block.
const problemPrefix = "# foonmail: "

// forwardedPrefix names an attachment carried over from a forwarded
// message, which exists only in memory.
const forwardedPrefix = "forwarded: "

const help = "# Lines starting with # are ignored up to the first blank line.\n" +
	"# Attach files with lines like: Attach: ~/Documents/file.pdf\n"

// EditorFile renders a draft as text for an external editor: headers, a
// blank line, then the body.
func EditorFile(d *Draft) []byte {
	var b bytes.Buffer
	b.WriteString(help)
	fmt.Fprintf(&b, "From: %s\n", d.From)
	fmt.Fprintf(&b, "To: %s\n", d.To)
	fmt.Fprintf(&b, "Cc: %s\n", d.Cc)
	fmt.Fprintf(&b, "Bcc: %s\n", d.Bcc)
	fmt.Fprintf(&b, "Subject: %s\n", d.Subject)
	for _, a := range d.Attachments {
		if a.Path != "" {
			fmt.Fprintf(&b, "Attach: %s\n", a.Path)
		} else {
			fmt.Fprintf(&b, "Attach: %s%s\n", forwardedPrefix, a.Name)
		}
	}
	b.WriteString("\n")
	b.WriteString(d.Body)
	if !strings.HasSuffix(d.Body, "\n") {
		b.WriteString("\n")
	}
	return b.Bytes()
}

// ParseEditorFile reads an edited file back into a copy of d (which keeps
// threading headers and forwarded attachments). Any problem means the
// result must not be used.
func ParseEditorFile(data []byte, d *Draft) (*Draft, []string) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	head, body, _ := strings.Cut(text, "\n\n")

	out := *d
	out.Attachments = nil
	var problems []string
	seen := map[string]bool{}
	var last *string

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
		case "from":
			out.From, last = value, &out.From
		case "to":
			out.To, last = value, &out.To
		case "cc":
			out.Cc, last = value, &out.Cc
		case "bcc":
			out.Bcc, last = value, &out.Bcc
		case "subject":
			out.Subject, last = value, &out.Subject
		case "attach":
			if value == "" {
				continue
			}
			if name, ok := strings.CutPrefix(value, forwardedPrefix); ok {
				found := false
				for _, a := range d.Attachments {
					if a.Path == "" && a.Name == name && !seen["fwd:"+name] {
						seen["fwd:"+name] = true
						out.Attachments = append(out.Attachments, a)
						found = true
						break
					}
				}
				if !found {
					problems = append(problems, "no forwarded attachment named "+name)
				}
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
				out.Attachments = append(out.Attachments, Attachment{Name: filepath.Base(p), Path: p})
			}
		default:
			problems = append(problems, fmt.Sprintf("unknown header %q", strings.TrimSpace(name)))
		}
	}
	out.Body = strings.TrimSuffix(body, "\n")
	return &out, problems
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

// annotate puts problem comments at the top of an edited file, replacing
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

// EditExternally writes the draft to a private temporary file, runs argv
// with the file's path appended, and parses the result. Problems (such as
// a missing attachment) reopen the editor with them listed at the top.
// Closing the editor without changes ends the loop.
func EditExternally(argv []string, d *Draft) (*Draft, EditOutcome, error) {
	if len(argv) == 0 {
		return d, 0, fmt.Errorf("no editor command")
	}
	f, err := os.CreateTemp(os.Getenv("XDG_RUNTIME_DIR"), "foonmail-draft-*.eml")
	if err != nil {
		return d, 0, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	content := EditorFile(d)
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
				return d, EditUnchanged, nil
			}
			return d, EditCancelled, nil
		}
		nd, problems := ParseEditorFile(edited, d)
		if len(problems) == 0 {
			return nd, EditApplied, nil
		}
		content = annotate(edited, problems)
	}
}
