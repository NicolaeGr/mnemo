package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"example.com/segments/internal/model"
)

// Retag applies add/remove of the actor's own book tags to a live contact
// (§5.5). Removing the system book is ignored; I1 is restored by re-adding the
// system book when nothing would remain. No change rows are written, but the
// sync_epoch of every principal whose visibility of the card changed is bumped
// (§6.3). Caller holds LockSync.
func (s ScopedStore) Retag(ctx context.Context, contactID int64, addBookIDs, removeBookIDs []int64) error {
	var one int
	err := s.Tx.QueryRow(ctx, `
		SELECT 1 FROM contacts WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		contactID, s.Actor.UserID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	if err != nil {
		return err
	}

	before, err := s.tagSet(ctx, contactID)
	if err != nil {
		return err
	}

	var systemID int64
	if err := s.Tx.QueryRow(ctx,
		`SELECT id FROM books WHERE owner_user_id = $1 AND is_system`, s.Actor.UserID,
	).Scan(&systemID); err != nil {
		return err
	}

	active, err := s.ActiveBooks(ctx)
	if err != nil {
		return err
	}

	after := applyRetag(before, systemID, active, addBookIDs, removeBookIDs)
	if err := s.replaceTags(ctx, contactID, after); err != nil {
		return err
	}

	principals, err := s.principalsWhoseVisibilityChanged(ctx, active, before, after)
	if err != nil {
		return err
	}
	_, err = BumpPrincipalsEpoch(ctx, s.Tx, principals)
	return err
}

// tagSet returns the live contact's current book ids.
func (s ScopedStore) tagSet(ctx context.Context, contactID int64) (map[int64]struct{}, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT book_id FROM contact_books WHERE contact_id = $1 FOR SHARE`, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := make(map[int64]struct{})
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = struct{}{}
	}
	return set, rows.Err()
}

// applyRetag computes the final tag set from §5.5 rules: only the actor's own
// active books may be added, system removal is dropped, and an empty result is
// repaired to just the system book (I1).
func applyRetag(before map[int64]struct{}, systemID int64, active []Book, addIDs, removeIDs []int64) map[int64]struct{} {
	after := make(map[int64]struct{}, len(before)+len(addIDs))
	for id := range before {
		after[id] = struct{}{}
	}
	for _, id := range addIDs {
		for _, b := range active {
			if b.ID == id {
				after[id] = struct{}{}
				break
			}
		}
	}
	for _, id := range removeIDs {
		if id != systemID { // system detach is ignored (§5.5)
			delete(after, id)
		}
	}
	if len(after) == 0 {
		after[systemID] = struct{}{} // I1 repair
	}
	return after
}

// replaceTags makes contact_books exactly equal to want for the contact.
func (s ScopedStore) replaceTags(ctx context.Context, contactID int64, want map[int64]struct{}) error {
	ids := setSlice(want)
	if _, err := s.Tx.Exec(ctx, `
		DELETE FROM contact_books WHERE contact_id = $1 AND NOT (book_id = ANY($2))`,
		contactID, ids); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := s.Tx.Exec(ctx, `
		INSERT INTO contact_books (contact_id, book_id)
		SELECT $1, unnest($2::bigint[])
		ON CONFLICT DO NOTHING`, contactID, ids)
	return err
}

// principalsWhoseVisibilityChanged returns the user's principals whose view of
// the card flipped between before and after (§6.3): a principal cares only
// whether the card sits in >= 1 book it can see (tier match, not disabled).
func (s ScopedStore) principalsWhoseVisibilityChanged(ctx context.Context, active []Book, before, after map[int64]struct{}) ([]int64, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT id, tier FROM principals WHERE user_id = $1`, s.Actor.UserID)
	if err != nil {
		return nil, err
	}
	type pRow struct {
		id   int64
		tier string
	}
	var principals []pRow
	for rows.Next() {
		var p pRow
		if err := rows.Scan(&p.id, &p.tier); err != nil {
			rows.Close()
			return nil, err
		}
		principals = append(principals, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Book ids this principal has explicitly disabled (overrides only subtract).
	disabled := make(map[int64]map[int64]struct{})
	overrides, err := s.Tx.Query(ctx, `
		SELECT o.principal_id, o.book_id
		  FROM principal_book_overrides o
		  JOIN principals p ON p.id = o.principal_id
		 WHERE p.user_id = $1 AND NOT o.enabled`, s.Actor.UserID)
	if err != nil {
		return nil, err
	}
	for overrides.Next() {
		var pid, bookID int64
		if err := overrides.Scan(&pid, &bookID); err != nil {
			overrides.Close()
			return nil, err
		}
		if disabled[pid] == nil {
			disabled[pid] = make(map[int64]struct{})
		}
		disabled[pid][bookID] = struct{}{}
	}
	overrides.Close()
	if err := overrides.Err(); err != nil {
		return nil, err
	}

	var bump []int64
	for _, p := range principals {
		visible := make(map[int64]struct{})
		for _, b := range active {
			if _, off := disabled[p.id][b.ID]; off {
				continue
			}
			if hasTierStr(b.SyncedTiers, p.tier) {
				visible[b.ID] = struct{}{}
			}
		}
		if anyIn(before, visible) != anyIn(after, visible) {
			bump = append(bump, p.id)
		}
	}
	return bump, nil
}

// anyIn reports whether any key of a is also in b.
func anyIn(a, b map[int64]struct{}) bool {
	for k := range a {
		if _, ok := b[k]; ok {
			return true
		}
	}
	return false
}

func hasTierStr(tiers []string, want string) bool {
	for _, t := range tiers {
		if t == want {
			return true
		}
	}
	return false
}

func setSlice(set map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}
