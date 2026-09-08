package store

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ContactSearch is a page of search results.
type ContactSearch struct {
	Contacts []Contact
	NextPage bool
}

// SearchParams bounds a contact search. Query is matched case-insensitively
// against FN via the trgm index; BookID, when non-nil, restricts to a single
// tag (join on contact_books). Page is 1-based.
type SearchParams struct {
	Query  string
	BookID *int64
	Page   int
	Per    int
}

// Search returns live contacts matching q (fn trigram match), scoped by the
// actor's user and, optionally, one book.
func (s ScopedStore) Search(ctx context.Context, p SearchParams) (ContactSearch, error) {
	per := p.Per
	if per <= 0 || per > 100 {
		per = 50
	}
	page := p.Page
	if page < 1 {
		page = 1
	}

	var rows pgx.Rows
	var err error
	q := strings.TrimSpace(p.Query)
	base := `
		SELECT id, filename, vcard_text, uid, search_meta, etag,
		       modified_by, deleted_at, created_at, updated_at
		  FROM contacts c`
	var args []any

	switch {
	case p.BookID != nil && q != "":
		base += `
		 WHERE c.user_id = $1 AND c.deleted_at IS NULL
		   AND c.id IN (SELECT contact_id FROM contact_books WHERE book_id = $2)
		   AND c.search_meta->>'fn' ILIKE '%' || $3 || '%'`
		args = []any{s.Actor.UserID, *p.BookID, q}
	case p.BookID != nil:
		base += `
		 WHERE c.user_id = $1 AND c.deleted_at IS NULL
		   AND c.id IN (SELECT contact_id FROM contact_books WHERE book_id = $2)`
		args = []any{s.Actor.UserID, *p.BookID}
	case q != "":
		base += `
		 WHERE c.user_id = $1 AND c.deleted_at IS NULL
		   AND c.search_meta->>'fn' ILIKE '%' || $2 || '%'`
		args = []any{s.Actor.UserID, q}
	default:
		base += `
		 WHERE c.user_id = $1 AND c.deleted_at IS NULL`
		args = []any{s.Actor.UserID}
	}

	base += ` ORDER BY c.filename LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
	args = append(args, per+1, (page-1)*per)

	rows, err = s.Tx.Query(ctx, base, args...)
	if err != nil {
		return ContactSearch{}, err
	}
	defer rows.Close()

	out := make([]Contact, 0, per)
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return ContactSearch{}, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return ContactSearch{}, err
	}

	next := false
	if len(out) > per {
		out = out[:per]
		next = true
	}
	return ContactSearch{Contacts: out, NextPage: next}, nil
}
