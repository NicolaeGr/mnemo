// Package vcard normalizes parsed cards and extracts their search index. Both
// the CardDAV layer and the REST API build the same search_meta from it.
package vcard

import (
	"bytes"
	"strings"

	"github.com/emersion/go-vcard"
)

// EnsureFormattedName gives the card an FN when absent, derived from N, then the
// email local part, then a placeholder. Cards without FN break clients and are
// unsearchable by name.
func EnsureFormattedName(card vcard.Card) {
	if card.Value(vcard.FieldFormattedName) != "" {
		return
	}
	if n := card.Name(); n != nil {
		name := strings.TrimSpace(n.GivenName + " " + n.FamilyName)
		if name != "" {
			card.SetValue(vcard.FieldFormattedName, name)
			return
		}
	}
	if email := card.Value(vcard.FieldEmail); email != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			card.SetValue(vcard.FieldFormattedName, email[:at])
			return
		}
	}
	card.SetValue(vcard.FieldFormattedName, "(unnamed)")
}

// CanonicalText re-encodes a card with CRs stripped, so the etag and the stored
// body always agree. Call EnsureFormattedName first.
func CanonicalText(card vcard.Card) string {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return ""
	}
	return NormalizeText(buf.String())
}

// NormalizeText strips carriage returns so bodies store as LF.
func NormalizeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// DeriveUID returns the card's UID, or the filename stem when absent.
func DeriveUID(card vcard.Card, filename string) string {
	if uid := card.Value(vcard.FieldUID); uid != "" {
		return uid
	}
	if i := strings.LastIndex(filename, ".vcf"); i > 0 {
		return filename[:i]
	}
	return filename
}

// SearchMeta builds the frozen search index shape from a card.
func SearchMeta(card vcard.Card) map[string]any {
	rawTels := card.Values(vcard.FieldTelephone)
	tels := make([]map[string]string, 0, len(rawTels))
	norm := make([]string, 0, len(rawTels))
	for _, raw := range rawTels {
		digits := NormalizePhone(raw)
		tels = append(tels, map[string]string{"raw": raw, "norm": digits})
		if digits != "" {
			norm = append(norm, digits)
		}
	}
	return map[string]any{
		"fn":       card.Value(vcard.FieldFormattedName),
		"emails":   card.Values(vcard.FieldEmail),
		"org":      card.Value(vcard.FieldOrganization),
		"tels":     tels,
		"tel_norm": norm,
	}
}

// NormalizePhone keeps only digits, which is what phone search matches against.
func NormalizePhone(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
