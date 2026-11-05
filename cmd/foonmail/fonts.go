package main

import (
	"os/exec"
	"strings"
	"sync"

	"gioui.org/font"
)

// emojiFamily is the system's color emoji font family, as fontconfig
// names it ("Noto Color Emoji"), or "" if there is none.
var emojiFamily = sync.OnceValue(func() string {
	out, err := exec.Command("fc-match", "-f", "%{family}", "emoji").Output()
	if err != nil {
		return ""
	}
	family, _, _ := strings.Cut(string(out), ",")
	family = strings.TrimSpace(family)
	if !strings.Contains(strings.ToLower(family), "emoji") {
		return "" // fontconfig fell back to an ordinary font
	}
	return family
})

// withEmoji adds the emoji font after a font family. Gio falls back only
// to families it is given or that fontconfig substitutes for them, and
// fontconfig doesn't name an emoji font for "monospace" or "sans-serif":
// without this, emoji show as missing glyphs or in black and white from
// whatever font happens to have them. The emoji font comes last, as it
// has its own digits and "#" for keycaps.
func withEmoji(family string) font.Typeface {
	if e := emojiFamily(); e != "" {
		return font.Typeface(family + ", " + e)
	}
	return font.Typeface(family)
}
