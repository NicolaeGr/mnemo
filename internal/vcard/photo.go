package vcard

import (
	"strings"

	"github.com/emersion/go-vcard"
)

// PhotoURL returns a card's PHOTO as a value a browser can display. An existing
// data or http(s) URL is returned unchanged; base64 bytes (vCard 3.0 with
// ENCODING=b, or a bare value) become an image data URL.
func PhotoURL(card vcard.Card) string {
	if card == nil {
		return ""
	}
	fields := card[vcard.FieldPhoto]
	if len(fields) == 0 || fields[0] == nil {
		return ""
	}
	f := fields[0]
	value := strings.TrimSpace(f.Value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") {
		return value
	}
	return "data:" + photoMIME(f.Params) + ";base64," + stripASCIIWhitespace(value)
}

func photoMIME(p vcard.Params) string {
	for _, key := range []string{vcard.ParamType, vcard.ParamMediaType} {
		raw := paramValue(p, key)
		for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		}) {
			switch strings.ToLower(strings.TrimSpace(token)) {
			case "jpeg", "jpg", "image/jpeg":
				return "image/jpeg"
			case "png", "image/png":
				return "image/png"
			case "webp", "image/webp":
				return "image/webp"
			case "gif", "image/gif":
				return "image/gif"
			}
		}
	}
	return "image/jpeg"
}

// paramValue is a case-insensitive Params lookup: clients may preserve their own
// parameter casing.
func paramValue(p vcard.Params, key string) string {
	if v := p.Get(key); v != "" {
		return v
	}
	for k, vs := range p {
		if strings.EqualFold(k, key) && len(vs) > 0 {
			return strings.Join(vs, ",")
		}
	}
	return ""
}

func stripASCIIWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return -1
		}
		return r
	}, s)
}
