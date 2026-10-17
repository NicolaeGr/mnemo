// Package vcard normalizes parsed cards and extracts their search index. Both
// the CardDAV layer and the REST API build the same search_meta from it.
package vcard

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-vcard"
)

// Unnamed is the FN given to a card that has no name parts and no usable email,
// so callers can tell "no name" from a real one.
const Unnamed = "(unnamed)"

// ErrEncode reports a card that could not be re-encoded (should not happen for
// a parsed card); callers treat it as a malformed-body precondition failure.
var ErrEncode = errors.New("vcard: could not encode card")

// ErrParse reports a body that is not a parseable vCard.
var ErrParse = errors.New("vcard: could not parse card")

// foldWidth is the RFC 6350 line-folding limit in octets.
const foldWidth = 75

// FromRaw canonicalizes a raw client vCard body for storage without re-encoding
// it structurally: line endings are normalized (CRLF/CR -> LF), overlong lines
// are re-folded, and a missing FN or UID is spliced in before END:VCARD. Every
// other property, its order, and its parameters survive verbatim, so the stored
// body differs from what the client sent only by line normalization and the
// mandatory FN/UID guarantee. The parsed card supplies semantics only (the
// derived FN/UID and the search index).
func FromRaw(raw, filename string) (text, uid string, meta []byte, err error) {
	card, perr := vcard.NewDecoder(strings.NewReader(raw)).Decode()
	if perr != nil {
		return "", "", nil, ErrParse
	}
	hadFN := card.Value(vcard.FieldFormattedName) != ""
	hadUID := card.Value(vcard.FieldUID) != ""

	EnsureFormattedName(card)
	uid = EnsureUID(card, filename)
	meta, jerr := json.Marshal(SearchMeta(card))
	if jerr != nil {
		return "", "", nil, jerr
	}

	var insert []string
	if !hadFN {
		insert = append(insert, "FN:"+escapeValue(card.Value(vcard.FieldFormattedName)))
	}
	if !hadUID {
		insert = append(insert, "UID:"+escapeValue(uid))
	}
	return normalizeBody(raw, insert), uid, meta, nil
}

// normalizeBody strips CRs, unfolds and re-folds every logical line, and splices
// insert before the END:VCARD terminator (or appends when there is none).
func normalizeBody(raw string, insert []string) string {
	logical := logicalLines(raw)
	out := make([]string, 0, len(logical)+len(insert))
	inserted := false
	for _, l := range logical {
		if !inserted && strings.EqualFold(strings.TrimSpace(l), "END:VCARD") {
			out = append(out, insert...)
			inserted = true
		}
		out = append(out, l)
	}
	if !inserted {
		out = append(out, insert...)
	}
	return foldLines(out)
}

// WithCategories returns a canonical body whose CATEGORIES properties list names,
// or carry none when names is empty. Each name gets its own property, because the
// go-webdav response path re-encodes through go-vcard, which would escape a comma
// list into one literal value. Every other property, its order, and its parameters
// survive; only line endings, folding, and CATEGORIES change.
func WithCategories(text string, names []string) string {
	logical := logicalLines(text)
	out := make([]string, 0, len(logical)+len(names))
	inserted := false
	for _, l := range logical {
		if propertyName(l) == "CATEGORIES" {
			continue
		}
		if !inserted && strings.EqualFold(strings.TrimSpace(l), "END:VCARD") {
			out = append(out, categoryLines(names)...)
			inserted = true
		}
		out = append(out, l)
	}
	if !inserted {
		out = append(out, categoryLines(names)...)
	}
	return foldLines(out)
}

// logicalLines normalizes line endings, drops a trailing blank line, and unfolds
// continuation lines into their logical properties.
func logicalLines(raw string) []string {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(raw, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return unfoldLines(lines)
}

func foldLines(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		for _, phys := range foldLine(l) {
			b.WriteString(phys)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// propertyName returns the uppercased property name of a logical line, before any
// group, parameter, or value.
func propertyName(line string) string {
	name := line
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	if i := strings.IndexByte(name, ';'); i >= 0 {
		name = name[:i]
	}
	return strings.ToUpper(strings.TrimSpace(name))
}

func categoryLines(names []string) []string {
	lines := make([]string, 0, len(names))
	for _, n := range names {
		lines = append(lines, "CATEGORIES:"+escapeValue(n))
	}
	return lines
}

// Categories reads the CATEGORIES properties of a canonical body. It returns the
// raw logical lines and the parsed values, so a caller can log what a client sent
// and in what shape. A value is parsed by splitting its line on unescaped commas
// and unescaping each part.
func Categories(text string) (raw, values []string) {
	for _, l := range logicalLines(text) {
		if propertyName(l) != "CATEGORIES" {
			continue
		}
		raw = append(raw, l)
		values = append(values, splitCategoryValue(lineValue(l))...)
	}
	return raw, values
}

// lineValue returns the part of a logical line after its first colon.
func lineValue(line string) string {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return line[i+1:]
	}
	return ""
}

// splitCategoryValue splits a CATEGORIES value on unescaped commas and unescapes
// each item.
func splitCategoryValue(v string) []string {
	out := make([]string, 0, 1)
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			if i+1 < len(v) {
				i++
				b.WriteByte(v[i])
			}
		case ',':
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(v[i])
		}
	}
	return append(out, b.String())
}

// unfoldLines joins continuation lines (those beginning with a space or tab) into
// their preceding logical line.
func unfoldLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		out = append(out, l)
	}
	return out
}

// foldLine folds one logical line into physical lines no longer than foldWidth
// octets, breaking on rune boundaries and prefixing continuation lines with one
// space.
func foldLine(s string) []string {
	if len(s) <= foldWidth {
		return []string{s}
	}
	var lines []string
	var b strings.Builder
	count := 0
	for _, r := range s {
		w := utf8.RuneLen(r)
		if count+w > foldWidth {
			lines = append(lines, b.String())
			b.Reset()
			b.WriteByte(' ')
			count = 1
		}
		b.WriteRune(r)
		count += w
	}
	lines = append(lines, b.String())
	return lines
}

// escapeValue escapes the characters a vCard property value must escape.
func escapeValue(v string) string {
	return strings.NewReplacer(
		"\\", "\\\\",
		";", "\\;",
		",", "\\,",
		"\n", "\\n",
		"\r", "",
	).Replace(v)
}

// Prepare produces everything a write needs from a card in one step: the
// canonical body (CRs stripped, FN and UID guaranteed), the effective UID, and
// the search index. The etag over the returned body always matches what is
// stored. Both the CardDAV and REST/admin write paths call this so their
// normalization cannot drift.
func Prepare(card vcard.Card, filename string) (text, uid string, meta []byte, err error) {
	EnsureFormattedName(card)
	uid = EnsureUID(card, filename)
	text = CanonicalText(card)
	if text == "" {
		return "", "", nil, ErrEncode
	}
	meta, err = json.Marshal(SearchMeta(card))
	if err != nil {
		return "", "", nil, err
	}
	return text, uid, meta, nil
}

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

// EnsureUID gives the card a UID property when absent, using the filename stem,
// and returns the effective UID. Cards without a UID break clients, and the
// filename is the stable key the delete tombstone is kept under.
func EnsureUID(card vcard.Card, filename string) string {
	if uid := card.Value(vcard.FieldUID); uid != "" {
		return uid
	}
	uid := filename
	if i := strings.LastIndex(filename, ".vcf"); i > 0 {
		uid = filename[:i]
	}
	card.SetValue(vcard.FieldUID, uid)
	return uid
}

// SearchMeta builds the search index from a card. The shape is fixed: fn, uid,
// n, sort, emails, org, tels, tel_norm. Adding a key here means also teaching
// search.go and the trigram indexes about it. Call EnsureUID first so uid is set.
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
		"uid":      card.Value(vcard.FieldUID),
		"n":        FormattedName(card.Name()),
		"sort":     SortName(card),
		"emails":   card.Values(vcard.FieldEmail),
		"org":      card.Value(vcard.FieldOrganization),
		"tels":     tels,
		"tel_norm": norm,
	}
}

// NormalizePhone keeps digits and a leading '+', and rewrites a leading "00"
// (the international prefix) to '+'. Phone search matches these digits, so a
// query like "5550100" still finds "+15550100".
func NormalizePhone(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '+' && b.Len() == 0:
			b.WriteByte(c)
		}
	}
	s := b.String()
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	return s
}
