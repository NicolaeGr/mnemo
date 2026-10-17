package contact

import (
	"encoding/base64"
	"strings"

	"github.com/emersion/go-vcard"

	"github.com/nicolaegr/mnemo/internal/model"
)

const (
	maxPhotoDataURL = 4 << 20
	maxPhotoBytes   = 2 << 20
)

// applyPhoto writes a newly selected image and honors an explicit removal. An
// empty Photo leaves the PHOTO copied from base untouched.
func applyPhoto(card vcard.Card, f Form) error {
	if f.RemovePhoto {
		delete(card, vcard.FieldPhoto)
		return nil
	}
	raw := f.Photo
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	mime, payload, err := parsePhotoDataURL(raw)
	if err != nil {
		return err
	}
	if strings.HasPrefix(card.Value(vcard.FieldVersion), "4") {
		card.SetValue(vcard.FieldPhoto, "data:"+mime+";base64,"+payload)
		return nil
	}
	card.Set(vcard.FieldPhoto, &vcard.Field{
		Value: payload,
		Params: vcard.Params{
			"ENCODING": {"b"},
			"TYPE":     {photoType(mime)},
		},
	})
	return nil
}

// parsePhotoDataURL accepts only base64 image/jpeg, png, webp, or gif data URLs
// within the size limits.
func parsePhotoDataURL(raw string) (mime, payload string, err error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > maxPhotoDataURL {
		return "", "", model.ErrPrecondition
	}
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", "", model.ErrPrecondition
	}
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return "", "", model.ErrPrecondition
	}
	header := strings.ToLower(raw[len("data:"):comma])
	parts := strings.Split(header, ";")
	mime = strings.TrimSpace(parts[0])
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return "", "", model.ErrPrecondition
	}
	base64Marker := false
	for _, p := range parts[1:] {
		if strings.TrimSpace(p) == "base64" {
			base64Marker = true
			break
		}
	}
	if !base64Marker {
		return "", "", model.ErrPrecondition
	}
	payload = stripBase64Whitespace(raw[comma+1:])
	decoded, derr := base64.StdEncoding.DecodeString(payload)
	if derr != nil || len(decoded) > maxPhotoBytes {
		return "", "", model.ErrPrecondition
	}
	return mime, payload, nil
}

func photoType(mime string) string {
	switch mime {
	case "image/png":
		return "PNG"
	case "image/webp":
		return "WEBP"
	case "image/gif":
		return "GIF"
	default:
		return "JPEG"
	}
}

func stripBase64Whitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return -1
		}
		return r
	}, s)
}
