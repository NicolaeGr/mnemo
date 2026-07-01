package pages

import (
	"crypto/sha1"
	"fmt"
	"strings"
)

// Deterministic HSL background from a name string — same name always gets same color
func avatarStyle(name string) string {
	h := sha1.Sum([]byte(name))
	hue := (int(h[0])*256 + int(h[1])) % 360
	sat := 45 + (int(h[2]) % 20)
	light := 40 + (int(h[3]) % 15)
	return fmt.Sprintf("background-color: hsl(%d, %d%%, %d%%)", hue, sat, light)
}

func initial(s string) string {
	if s == "" {
		return "?"
	}
	runes := []rune(s)
	return strings.ToUpper(string(runes[0]))
}
