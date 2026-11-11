package main

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"foonmail/internal/render"
)

// attachment opens or saves an attachment of the current message: the one
// whose number was typed, or the only one.
func (u *ui) attachment(o *openConv, save bool) {
	typed := o.linkInput
	o.linkInput = ""
	p := o.acting()
	if p.rendered == nil {
		u.notice = "The message is still loading"
		return
	}
	atts := listedAttachments(p.rendered)
	key := "o"
	if save {
		key = "w"
	}
	var a render.Attachment
	switch n, err := strconv.Atoi(typed); {
	case len(atts) == 0:
		u.notice = "This message has no attachments"
		return
	case typed == "" && len(atts) > 1:
		u.notice = fmt.Sprintf("Type the attachment's number (1–%d), then %s", len(atts), key)
		return
	case typed == "":
		a = atts[0]
	case err != nil || n < 1 || n > len(atts):
		u.notice = fmt.Sprintf("No attachment #%s in this message", typed)
		return
	default:
		a = atts[n-1]
	}
	name := attachmentName(a)
	if !save && render.RiskyToOpen(a) {
		u.notice = fmt.Sprintf("Not opening %s: it could run as a program or load remote content. w saves it.", name)
		return
	}
	u.notice = "Getting " + name + "…"
	go func() {
		path, err := u.writeAttachment(p, a, name, save)
		u.post(func() {
			if err == nil && !save {
				err = u.openURL(path)
			}
			switch {
			case err != nil:
				u.notice = fmt.Sprintf("Couldn't %s %s: %v", map[bool]string{false: "open", true: "save"}[save], name, err)
			case save:
				u.notice = "Saved " + path
			default:
				u.notice = "Opened " + name
			}
		})
	}()
}

// writeAttachment writes an attachment to the downloads directory, or to
// a private temporary directory for opening.
func (u *ui) writeAttachment(p *part, a render.Attachment, name string, save bool) (string, error) {
	raw, err := u.loadRaw(p.entry.Summary)
	if err != nil {
		return "", err
	}
	data, err := render.AttachmentData(raw, a.Index)
	if err != nil {
		return "", err
	}
	if save {
		return writeNew(u.downloads(), name, data)
	}
	dir, err := openDir()
	if err != nil {
		return "", err
	}
	return writeNew(dir, name, data)
}

// openInBrowser shows the current message's HTML in the web browser,
// loading nothing from the network.
func (u *ui) openInBrowser(o *openConv) {
	o.linkInput = ""
	p := o.acting()
	m := p.rendered
	switch {
	case m == nil:
		u.notice = "The message is still loading"
		return
	case !m.HasHTML:
		u.notice = "This message has no HTML version"
		return
	}
	go func() {
		path, err := func() (string, error) {
			raw, err := u.loadRaw(p.entry.Summary)
			if err != nil {
				return "", err
			}
			page, err := render.BrowserPage(m, raw)
			if err != nil {
				return "", err
			}
			dir, err := openDir()
			if err != nil {
				return "", err
			}
			title := strings.TrimSpace(m.Subject)
			if title == "" {
				title = "message"
			}
			return writeNew(dir, render.SafeFilename(title)+".html", page)
		}()
		u.post(func() {
			if err == nil {
				err = u.openURL(path)
			}
			if err != nil {
				u.notice = "Couldn't open the HTML: " + err.Error()
				return
			}
			u.notice = "Opened the HTML in the browser; remote images and scripts are blocked"
		})
	}()
}

// attachmentName is the name to write an attachment under: the sender's,
// made safe, with an extension for its type if it has none, so the
// desktop knows what opens it.
func attachmentName(a render.Attachment) string {
	name := render.SafeFilename(a.Filename)
	if filepath.Ext(name) == "" {
		if exts, _ := mime.ExtensionsByType(a.ContentType); len(exts) > 0 {
			name += exts[0]
		}
	}
	return name
}

// openDir is a new private directory for files handed to other programs,
// under $XDG_RUNTIME_DIR, which is emptied at logout.
func openDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	base = filepath.Join(base, "foonmail")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "open-")
}

// writeNew writes data to dir/name without replacing a file there: if the
// name is taken, "name (2).ext" and so on.
func writeNew(dir, name string, data []byte) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		path := filepath.Join(dir, name)
		if i > 1 {
			path = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("too many files named %s in %s", name, dir)
}

// downloadDir is the user's download directory (xdg-user-dir DOWNLOAD),
// or ~/Downloads, or the home directory.
func downloadDir() string {
	if out, err := exec.Command("xdg-user-dir", "DOWNLOAD").Output(); err == nil {
		if dir := strings.TrimSpace(string(out)); dir != "" && dirExists(dir) {
			return dir
		}
	}
	home, _ := os.UserHomeDir()
	if dir := filepath.Join(home, "Downloads"); dirExists(dir) {
		return dir
	}
	return home
}

func dirExists(dir string) bool {
	st, err := os.Stat(dir)
	return err == nil && st.IsDir()
}
