package vcard

import (
	"strings"

	"github.com/emersion/go-vcard"
)

// SortName returns a family-name-first ordering key for a card. It is used only
// for sorting; the display FN is left untouched. When the card has no name
// parts, it falls back to the formatted name.
func SortName(card vcard.Card) string {
	if card == nil {
		return ""
	}
	if n := card.Name(); n != nil {
		parts := []string{
			n.FamilyName,
			n.GivenName,
			n.AdditionalName,
			n.HonorificPrefix,
			n.HonorificSuffix,
		}
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return strings.Join(out, " ")
		}
	}
	return card.Value(vcard.FieldFormattedName)
}
