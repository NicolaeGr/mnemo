package pages

import (
	"fmt"
	"strings"
	"unicode"
)

// avatarColor returns a deterministic OKLCH colour for a name. Keep the hash and
// ranges in sync with avatarColor in assets/app.js so the editor preview matches
// the rendered avatar.
func avatarColor(name string) string {
	h := uint32(2166136261)
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		h ^= uint32(r)
		h *= 16777619
	}
	hue := h % 360
	light := 0.56 + float64((h/360)%12)/100
	chroma := 0.12 + float64((h/4320)%7)/100
	return fmt.Sprintf("oklch(%.2f %.3f %d)", light, chroma, hue)
}

// avatarInitial prefers the given name, then the full display name.
func avatarInitial(given, name string) string {
	for _, s := range []string{given, name} {
		for _, r := range strings.TrimSpace(s) {
			return string(unicode.ToUpper(r))
		}
	}
	return "?"
}
