package store

import (
	"context"
	"fmt"
	"strings"
)

// ContactSearch is a page of search results. Total is the number of matching
// contacts across every page, so the UI can show a count and a pager.
type ContactSearch struct {
	Contacts []Contact
	NextPage bool
	Total    int
}

// SearchParams bounds a contact search. Query matches name, org, or phone
// digits; BookID, when non-nil, restricts to a single tag. Page is 1-based.
// Sort is one of name, org, updated, created (default name); Dir is asc or desc.
type SearchParams struct {
	Query  string
	BookID *int64
	Page   int
	Per    int
	Sort   string
	Dir    string
}

// Search returns live contacts matching q, scoped by the actor's user and,
// optionally, one book, in the requested order.
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
	conds = append(conds, "c.user_id = $1 AND c.deleted_at IS NULL")
	if p.BookID != nil {
		conds = append(conds, fmt.Sprintf(
			"c.id IN (SELECT contact_id FROM contact_books WHERE book_id = $%d)", arg(*p.BookID)))
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		lit := arg(q)
		or := []string{
			fmt.Sprintf("c.search_meta->>'fn' ILIKE '%%' || $%d || '%%'", lit),
			fmt.Sprintf("c.search_meta->>'n' ILIKE '%%' || $%d || '%%'", lit),
			fmt.Sprintf("c.search_meta->>'org' ILIKE '%%' || $%d || '%%'", lit),
		}
		if digits := phoneDigits(q); digits != "" {
			// Match each normalized number on its own. Comparing against the
			// serialized JSON array (search_meta->>'tel_norm') can match across
			// two numbers, and no index serves that text form.
			or = append(or, fmt.Sprintf(
				"EXISTS (SELECT 1 FROM jsonb_array_elements_text(c.search_meta->'tel_norm') AS tel WHERE tel ILIKE '%%' || $%d || '%%')",
				arg(digits)))
		}
		conds = append(conds, "("+strings.Join(or, " OR ")+")")
	}

	order := sortExpr(p.Sort) + " " + sortDir(p.Dir)

	query := fmt.Sprintf(`
		SELECT id, filename, vcard_text, uid, search_meta, etag,
		       modified_by, deleted_at, created_at, updated_at,
		       count(*) OVER() AS total
		  FROM contacts c
		 WHERE %s
		 ORDER BY %s, c.filename
		 LIMIT $%d OFFSET $%d`,
		strings.Join(conds, " AND "), order, arg(per+1), arg((page-1)*per))

	rows, err := s.Tx.Query(ctx, query, args...)
	if err != nil {
		return ContactSearch{}, err
	}
	defer rows.Close()

	out := make([]Contact, 0, per)
	total := 0
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Filename, &c.VCardText, &c.UID, &c.SearchMeta,
			&c.ETag, &c.ModifiedBy, &c.DeletedAt, &c.CreatedAt, &c.UpdatedAt, &total); err != nil {
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
	return ContactSearch{Contacts: out, NextPage: next, Total: total}, nil
}

// sortExpr maps a UI sort key onto a safe SQL expression. Unknown keys fall back
// to the display name, so a bad query string can never reach SQL as a column.
func sortExpr(key string) string {
	switch key {
	case "org":
		return "lower(coalesce(c.search_meta->>'org', ''))"
	case "updated":
		return "c.updated_at"
	case "created":
		return "c.created_at"
	default:
		return "lower(coalesce(c.search_meta->>'fn', ''))"
	}
}

func sortDir(dir string) string {
	if strings.EqualFold(dir, "desc") {
		return "DESC"
	}
	return "ASC"
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
