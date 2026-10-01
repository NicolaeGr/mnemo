// Package vcard normalizes parsed cards and extracts their search index. Both
// the CardDAV layer and the REST API build the same search_meta from it.
package vcard

import (
	"bytes"
	"strings"

	"github.com/emersion/go-vcard"
)

// Unnamed is the FN given to a card that has no name parts and no usable email,
// so callers can tell "no name" from a real one.
const Unnamed = "(unnamed)"

// EnsureFormattedName gives the card an FN when absent, derived from N, then the
// email local part, then a placeholder. Cards without FN break clients and are
// unsearchable by name.
func EnsureFormattedName(card vcard.Card) {
	if card.Value(vcard.FieldFormattedName) != "" {
		return
	}
	if name := FormattedName(card.Name()); name != "" {
		card.SetValue(vcard.FieldFormattedName, name)
		return
	}
	if email := card.Value(vcard.FieldEmail); email != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			card.SetValue(vcard.FieldFormattedName, email[:at])
			return
		}
	}
	card.SetValue(vcard.FieldFormattedName, Unnamed)
}

// FormattedName joins a structured N into a display name.
func FormattedName(n *vcard.Name) string {
	if n == nil {
		return ""
	}
	parts := []string{n.HonorificPrefix, n.GivenName, n.AdditionalName, n.FamilyName, n.HonorificSuffix}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, " ")
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

// SearchMeta builds the search index from a card. The shape is fixed: fn, n,
// emails, org, tels, tel_norm. Adding a key here means also teaching search.go
// and the trigram indexes about it.
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
		"n":        FormattedName(card.Name()),
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
