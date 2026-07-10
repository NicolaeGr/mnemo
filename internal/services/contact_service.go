package services

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"mnemo/internal/db"
	"mnemo/internal/models"
	"mnemo/internal/vcard"
)

var ErrContactNotFound = errors.New("contact not found")

type ContactService struct {
	store *db.Store
}

func NewContactService(store *db.Store) *ContactService {
	return &ContactService{store: store}
}

func etag(vcardText string) string {
	h := sha1.Sum([]byte(vcardText))
	return fmt.Sprintf("\"%x\"", h)
}

func (s *ContactService) CountAll(ctx context.Context) (int, error) {
	var count int
	err := s.store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM contacts WHERE deleted_at IS NULL").Scan(&count)
	return count, err
}

func (s *ContactService) ListByBook(ctx context.Context, bookID string) ([]*models.Contact, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT id, address_book_id, display_name, vcard_text, etag, last_modified
		 FROM contacts
		 WHERE address_book_id = ? AND deleted_at IS NULL
		 ORDER BY display_name ASC`, bookID,
	)
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	defer rows.Close()

	var contacts []*models.Contact
	for rows.Next() {
		c := &models.Contact{}
		var lastMod string
		if err := rows.Scan(&c.ID, &c.AddressBookID, &c.DisplayName, &c.VCardText, &c.ETag, &lastMod); err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		c.LastModified, _ = time.Parse(time.DateTime, lastMod)
		if c.VCardText != "" {
			vc := vcard.Parse(c.VCardText)
			if len(vc.Phones) > 0 {
				c.FirstPhone = vc.Phones[0].Value
			}
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

func (s *ContactService) GetByID(ctx context.Context, id string) (*models.Contact, error) {
	c := &models.Contact{}
	var lastMod string
	err := s.store.DB.QueryRowContext(ctx,
		`SELECT id, address_book_id, display_name, vcard_text, etag, last_modified
		 FROM contacts WHERE id = ? AND deleted_at IS NULL`, id,
	).Scan(&c.ID, &c.AddressBookID, &c.DisplayName, &c.VCardText, &c.ETag, &lastMod)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrContactNotFound
		}
		return nil, fmt.Errorf("get contact: %w", err)
	}
	c.LastModified, _ = time.Parse(time.DateTime, lastMod)
	return c, nil
}

func buildVCard(displayName string) string {
	return fmt.Sprintf(`BEGIN:VCARD
VERSION:3.0
FN:%s
N:;%s;;;
END:VCARD`, displayName, displayName)
}

func (s *ContactService) Create(ctx context.Context, bookID, displayName, vcardText string) (*models.Contact, error) {
	if vcardText == "" {
		vcardText = buildVCard(displayName)
	}

	id := generateID()
	e := etag(vcardText)
	now := time.Now().Format(time.DateTime)

	_, err := s.store.DB.ExecContext(ctx,
		`INSERT INTO contacts (id, address_book_id, display_name, vcard_text, etag, last_modified)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, bookID, displayName, vcardText, e, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create contact: %w", err)
	}

	return &models.Contact{
		ID:            id,
		AddressBookID: bookID,
		DisplayName:   displayName,
		VCardText:     vcardText,
		ETag:          e,
		LastModified:  time.Now(),
	}, nil
}

func avatarSVG(name string) []byte {
	h := sha1.Sum([]byte(name))
	hue := (int(h[0])*256 + int(h[1])) % 360
	sat := 45 + (int(h[2]) % 20)
	light := 40 + (int(h[3]) % 15)

	initial := "?"
	if name != "" {
		runes := []rune(name)
		initial = strings.ToUpper(string(runes[0]))
	}

	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="128" height="128" viewBox="0 0 128 128">
  <rect width="128" height="128" rx="64" fill="hsl(%d,%d%%,%d%%)"/>
  <text x="64" y="50%%" dominant-baseline="central" text-anchor="middle" font-family="-apple-system,BlinkMacSystemFont,sans-serif" font-size="48" font-weight="700" fill="white">%s</text>
</svg>`, hue, sat, light, initial)
	return []byte(svg)
}

func (s *ContactService) GetPhoto(ctx context.Context, contactID string) (data []byte, mimeType string, etag string, displayName string, err error) {
	c, err := s.GetByID(ctx, contactID)
	if err != nil {
		return nil, "", "", "", err
	}
	vc := vcard.Parse(c.VCardText)
	if vc.Photo == "" {
		return avatarSVG(vc.DisplayName), "image/svg+xml", c.ETag, vc.DisplayName, nil
	}
	raw := vc.Photo
	mimeType = vc.PhotoType
	if mimeType == "" {
		mimeType = "image/jpeg"
	}

	// Strip data: URI prefix if present
	if strings.HasPrefix(raw, "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma >= 0 {
			before := raw[5:comma] // skip "data:"
			if semi := strings.IndexByte(before, ';'); semi >= 0 {
				mimeType = before[:semi]
			}
			raw = raw[comma+1:]
		}
	}

	// Strip non-base64 chars (vCards sometimes embed whitespace/newlines)
	raw = strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(raw)
	decoded := make([]byte, len(raw)*3/4)
	n, decErr := base64.StdEncoding.Decode(decoded, []byte(raw))
	if decErr != nil {
		n, decErr = base64.RawStdEncoding.Decode(decoded, []byte(raw))
		if decErr != nil {
			return nil, "", c.ETag, vc.DisplayName, fmt.Errorf("decode photo: %w", decErr)
		}
	}
	return decoded[:n], mimeType, c.ETag, vc.DisplayName, nil
}

func (s *ContactService) UpdateContact(ctx context.Context, id, displayName, vcardText string) (*models.Contact, error) {
	e := etag(vcardText)
	now := time.Now().Format(time.DateTime)

	res, err := s.store.DB.ExecContext(ctx,
		`UPDATE contacts SET display_name = ?, vcard_text = ?, etag = ?, last_modified = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		displayName, vcardText, e, now, id,
	)
	if err != nil {
		return nil, fmt.Errorf("update contact: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrContactNotFound
	}

	return &models.Contact{
		ID:           id,
		DisplayName:  displayName,
		VCardText:    vcardText,
		ETag:         e,
		LastModified: time.Now(),
	}, nil
}

func (s *ContactService) ReplaceAll(ctx context.Context, bookID string) error {
	_, err := s.store.DB.ExecContext(ctx, "DELETE FROM contacts WHERE address_book_id = ?", bookID)
	return err
}

func (s *ContactService) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().Format(time.DateTime)
	res, err := s.store.DB.ExecContext(ctx,
		"UPDATE contacts SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL", now, id,
	)
	if err != nil {
		return fmt.Errorf("soft delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrContactNotFound
	}
	return nil
}
