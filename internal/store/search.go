package store

import (
	"context"
	"fmt"
	"strings"
)

// ContactSearch is a page of search results.
type ContactSearch struct {
	Contacts []Contact
	NextPage bool
}

// SearchParams bounds a contact search. Query matches name or phone digits;
// BookID, when non-nil, restricts to a single tag. Page is 1-based.
type SearchParams struct {
	Query  string
	BookID *int64
	Page   int
	Per    int
}

// Search returns live contacts matching q by name or by phone digits, scoped by
// the actor's user and, optionally, one book.
func (s ScopedStore) Search(ctx context.Context, p SearchParams) (ContactSearch, error) {
	per := p.Per
	if per <= 0 || per > 100 {
		per = 50
	}
	page := p.Page
	if page < 1 {
		page = 1
	}

	var conds []string
	args := []any{s.Actor.UserID}
	arg := func(v any) int {
		args = append(args, v)
		return len(args)
	}
	conds = append(conds, fmt.Sprintf("c.user_id = $1 AND c.deleted_at IS NULL"))
	if p.BookID != nil {
		conds = append(conds, fmt.Sprintf(
			"c.id IN (SELECT contact_id FROM contact_books WHERE book_id = $%d)", arg(*p.BookID)))
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		fn := fmt.Sprintf("c.search_meta->>'fn' ILIKE '%%' || $%d || '%%'", arg(q))
		if digits := phoneDigits(q); digits != "" {
			tel := fmt.Sprintf("c.search_meta->>'tel_norm' ILIKE '%%' || $%d || '%%'", arg(digits))
			fn = "(" + fn + " OR " + tel + ")"
		}
		conds = append(conds, fn)
	}

	query := fmt.Sprintf(`
		SELECT id, filename, vcard_text, uid, search_meta, etag,
		       modified_by, deleted_at, created_at, updated_at
		  FROM contacts c
		 WHERE %s
		 ORDER BY c.filename
		 LIMIT $%d OFFSET $%d`,
		strings.Join(conds, " AND "), arg(per+1), arg((page-1)*per))

	rows, err := s.Tx.Query(ctx, query, args...)
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

func phoneDigits(q string) string {
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if c := q[i]; c >= '0' && c <= '9' {
			b.WriteByte(c)
		}
	}
	return b.String()
}
