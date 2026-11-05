package render

import (
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
)

// ErrNoHTML is returned by BrowserPage for a message without an HTML part.
var ErrNoHTML = errors.New("the message has no HTML version")

// browserHead goes before the mail's own HTML. The charset comes first,
// since the first declaration wins and the HTML is already decoded to
// UTF-8. The policy lets the page load nothing: no scripts, no remote
// images or styles (which would tell the sender it was read), no forms.
// Images embedded in the mail are inlined as data: addresses.
const browserHead = `<meta charset="utf-8">` +
	`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; ` +
	`img-src data:; style-src 'unsafe-inline'; font-src data:; form-action 'none'; base-uri 'none'">` + "\n"

// BrowserPage is the message's HTML made safe to open in a web browser
// from a file: it loads nothing from the network and runs no scripts, and
// the images embedded in the mail (cid: references) are inlined. raw is
// the message m was rendered from.
func BrowserPage(m *Message, raw []byte) ([]byte, error) {
	if !m.HasHTML {
		return nil, ErrNoHTML
	}
	page := m.HTML
	for _, a := range m.Attachments {
		if a.ContentID == "" || !strings.Contains(page, "cid:"+a.ContentID) {
			continue
		}
		data, err := AttachmentData(raw, a.Index)
		if err != nil {
			continue // shown as a missing image
		}
		uri := "data:" + a.ContentType + ";base64," + base64.StdEncoding.EncodeToString(data)
		page = strings.ReplaceAll(page, "cid:"+a.ContentID, uri)
	}
	return []byte(browserHead + page), nil
}

// riskyToOpen lists file types that opening could run as a program, or
// hand to one that runs it.
var riskyToOpen = map[string]bool{
	".desktop": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".csh": true,
	".py": true, ".pl": true, ".rb": true, ".php": true, ".js": true, ".jar": true,
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".msi": true, ".scr": true, ".ps1": true, ".vbs": true,
	".appimage": true, ".run": true, ".bin": true, ".elf": true, ".so": true, ".deb": true, ".rpm": true,
	".flatpakref": true, ".flatpakrepo": true, ".lnk": true, ".url": true, ".html": true, ".htm": true,
	".svg": true, ".xhtml": true, ".mht": true, ".hta": true,
}

var riskyTypes = []string{
	"application/x-desktop", "application/x-executable", "application/x-sharedlib",
	"application/x-shellscript", "application/x-sh", "application/x-msdownload",
	"application/x-msdos-program", "application/java-archive", "text/html",
	"application/xhtml+xml", "image/svg+xml", "application/javascript", "text/javascript",
}

// RiskyToOpen reports whether an attachment shouldn't be handed to the
// desktop's opener: programs and scripts, launchers, and web pages (which
// would load remote content or run scripts). Such files can still be saved.
func RiskyToOpen(a Attachment) bool {
	if riskyToOpen[strings.ToLower(filepath.Ext(SafeFilename(a.Filename)))] {
		return true
	}
	ct := strings.ToLower(a.ContentType)
	for _, t := range riskyTypes {
		if ct == t {
			return true
		}
	}
	return false
}
