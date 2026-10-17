package store

import (
	"context"
	"fmt"
	"strings"
)

// ExportContact is a live contact plus the display names of the books it is
// tagged into, ordered by slug. The names let an export rewrite CATEGORIES
// without loading the books again.
type ExportContact struct {
	Contact
	BookNames []string
}

// ExportContacts returns the actor's live contacts, optionally limited to one
// book or to a set of contact ids. An empty ids slice means every contact.
func (s ScopedStore) ExportContacts(ctx context.Context, bookID *int64, ids []int64) ([]ExportContact, error) {
	where, args := s.exportWhere(bookID, ids)
	query := fmt.Sprintf(`
		SELECT c.id, c.filename, c.vcard_text, c.uid, c.search_meta, c.etag,
		       c.modified_by, c.deleted_at, c.created_at, c.updated_at,
		       coalesce(array_agg(b.display_name ORDER BY b.slug)
		                FILTER (WHERE b.id IS NOT NULL), '{}')
		  FROM contacts c
		  LEFT JOIN contact_books cb ON cb.contact_id = c.id
		  LEFT JOIN books b ON b.id = cb.book_id AND b.owner_user_id = $1
		 WHERE %s
		 GROUP BY c.id
		 ORDER BY c.filename`, where)

	rows, err := s.Tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ExportContact, 0)
	for rows.Next() {
		var e ExportContact
		if err := rows.Scan(&e.ID, &e.Filename, &e.VCardText, &e.UID, &e.SearchMeta,
			&e.ETag, &e.ModifiedBy, &e.DeletedAt, &e.CreatedAt, &e.UpdatedAt,
			&e.BookNames); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExportNames returns up to limit display names for the same selection
// ExportContacts uses, ordered by display name. The export modal needs only a
// few names and the count up to the limit, so it never loads full cards.
func (s ScopedStore) ExportNames(ctx context.Context, bookID *int64, ids []int64, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 4
	}
	where, args := s.exportWhere(bookID, ids)
	args = append(args, limit)
	query := fmt.Sprintf(`
		SELECT coalesce(nullif(c.search_meta->>'fn', ''), c.uid)
		  FROM contacts c
		 WHERE %s
		 ORDER BY lower(coalesce(nullif(c.search_meta->>'fn', ''), c.uid)), c.filename
		 LIMIT $%d`, where, len(args))

	rows, err := s.Tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names := make([]string, 0, limit)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// exportWhere builds the shared selection predicate and its arguments. $1 is
// always the acting user.
func (s ScopedStore) exportWhere(bookID *int64, ids []int64) (string, []any) {
	conds := []string{"c.user_id = $1", "c.deleted_at IS NULL"}
	args := []any{s.Actor.UserID}
	ph := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if bookID != nil {
		conds = append(conds, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM contact_books x WHERE x.contact_id = c.id AND x.book_id = %s)", ph(*bookID)))
	}
	if len(ids) > 0 {
		conds = append(conds, fmt.Sprintf("c.id = ANY(%s::bigint[])", ph(ids)))
	}
	return strings.Join(conds, " AND "), args
}
