package store

import (
	"context"
	"errors"
	"log"

	"github.com/jackc/pgx/v5"

	"github.com/nicolaegr/mnemo/internal/model"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

// applyCategories rewrites a contact's CATEGORIES to the display names of its
// books and returns the resulting etag. A change rewrites the body and etag and
// appends a 'put'. When clientCats is set, the caller's CATEGORIES is logged
// before it is overwritten, so a client clearing its categories shows up as an
// empty list. Caller holds LockSync.
func (s ScopedStore) applyCategories(ctx context.Context, filename string, clientCats bool) (string, error) {
	var contactID int64
	var text, etag string
	err := s.Tx.QueryRow(ctx, `
		SELECT id, vcard_text, etag FROM contacts
		 WHERE user_id = $1 AND filename = $2 AND deleted_at IS NULL`,
		s.Actor.UserID, filename).Scan(&contactID, &text, &etag)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", model.ErrNotFound
	}
	if err != nil {
		return "", err
	}

	names, err := s.bookCategoryNames(ctx, contactID)
	if err != nil {
		return "", err
	}
	if clientCats {
		raw, sent := vcardmeta.Categories(text)
		log.Printf("categories: %s: client sent raw=%q parsed=%q, books give %q", filename, raw, sent, names)
	}
	next := vcardmeta.WithCategories(text, names)
	if next == text {
		return etag, nil
	}
	newETag := sha256Hex(next)
	if _, err := s.Tx.Exec(ctx, `
		UPDATE contacts SET vcard_text = $2, etag = $3, updated_at = now()
		 WHERE id = $1`, contactID, next, newETag); err != nil {
		return "", err
	}
	if err := AppendChange(ctx, s.Tx, s.Actor.UserID, filename, "put"); err != nil {
		return "", err
	}
	return newETag, nil
}

// bookCategoryNames returns the display names of the contact's books, ordered by
// slug so a book reorder never changes a stored body.
func (s ScopedStore) bookCategoryNames(ctx context.Context, contactID int64) ([]string, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT b.display_name
		  FROM contact_books cb
		  JOIN books b ON b.id = cb.book_id
		 WHERE cb.contact_id = $1 AND b.owner_user_id = $2
		 ORDER BY b.slug`, contactID, s.Actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// refreshBookCategories reapplies categories to every live contact tagged in the
// book after the book's name changed.
func (s ScopedStore) refreshBookCategories(ctx context.Context, bookID int64) error {
	files, err := s.filenamesInBook(ctx, bookID)
	if err != nil {
		return err
	}
	for _, f := range files {
		if _, err := s.applyCategories(ctx, f, false); err != nil {
			return err
		}
	}
	return nil
}

func (s ScopedStore) filenamesInBook(ctx context.Context, bookID int64) ([]string, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT c.filename
		  FROM contact_books cb
		  JOIN contacts c ON c.id = cb.contact_id
		 WHERE cb.book_id = $1 AND c.deleted_at IS NULL
		 ORDER BY c.filename`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := make([]string, 0)
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}
