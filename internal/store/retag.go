package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/nicolaegr/mnemo/internal/model"
)

// Retag applies add/remove of the actor's own book tags to a live contact.
// Removing every book leaves the contact untagged. The card's CATEGORIES is
// rewritten to follow the new book set, which appends a 'put' when it changes;
// the sync_epoch of every principal whose visibility of the card changed is also
// bumped. Caller holds LockSync.
func (s ScopedStore) Retag(ctx context.Context, contactID int64, addBookIDs, removeBookIDs []int64) error {
	var filename string
	err := s.Tx.QueryRow(ctx, `
		SELECT filename FROM contacts WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		contactID, s.Actor.UserID).Scan(&filename)
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

	active, err := s.ActiveBooks(ctx)
	if err != nil {
		return err
	}

	after := applyRetag(before, active, addBookIDs, removeBookIDs)
	if err := s.replaceTags(ctx, contactID, after); err != nil {
		return err
	}
	if _, err := s.applyCategories(ctx, filename, false); err != nil {
		return err
	}

	principals, err := s.principalsWhoseVisibilityChanged(ctx, active, before, after)
	if err != nil {
		return err
	}
	_, err = BumpPrincipalsEpoch(ctx, s.Tx, principals)
	return err
}

// SetContactBooks makes the live contact's tag set exactly want, diffing against
// the current set so a no-op save writes nothing. Caller holds LockSync.
func (s ScopedStore) SetContactBooks(ctx context.Context, contactID int64, want []int64) error {
	current, err := s.tagSet(ctx, contactID)
	if err != nil {
		return err
	}
	wantSet := make(map[int64]struct{}, len(want))
	var add, remove []int64
	for _, id := range want {
		wantSet[id] = struct{}{}
		if _, ok := current[id]; !ok {
			add = append(add, id)
		}
	}
	for id := range current {
		if _, ok := wantSet[id]; !ok {
			remove = append(remove, id)
		}
	}
	return s.Retag(ctx, contactID, add, remove)
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

// applyRetag computes the final tag set: only the actor's own active books may
// be added, and an empty result stays empty (the contact is untagged).
func applyRetag(before map[int64]struct{}, active []Book, addIDs, removeIDs []int64) map[int64]struct{} {
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
		delete(after, id)
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
// the card flipped between before and after. A principal sees an untagged card,
// and sees a tagged card through any book it can see (tier match, not disabled).
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
		if cardVisible(before, visible) != cardVisible(after, visible) {
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

// cardVisible reports whether a card in these books is visible. An untagged
// card is visible to every principal.
func cardVisible(tags, visible map[int64]struct{}) bool {
	return len(tags) == 0 || anyIn(tags, visible)
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
