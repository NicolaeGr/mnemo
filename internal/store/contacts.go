package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/model"
)

// Contact is one CardDAV address object row.
type Contact struct {
	ID         int64
	Filename   string
	VCardText  string
	UID        string
	SearchMeta []byte
	ETag       string
	ModifiedBy *int64
	DeletedAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Precondition carries the request's conditional headers.
type Precondition struct {
	IfMatch        *string // quoted etag or "*"; nil = absent
	IfNoneMatchAll bool    // If-None-Match: *
}

// PutContactParams feeds a PUT.
type PutContactParams struct {
	Filename   string
	UID        string
	VCardText  string // raw body; CRs are normalized here
	SearchMeta []byte
}

// PutResult reports the outcome of a PUT.
type PutResult struct {
	Created bool
	ETag    string
}

func scanContact(row pgx.Row) (Contact, error) {
	var c Contact
	err := row.Scan(&c.ID, &c.Filename, &c.VCardText, &c.UID, &c.SearchMeta,
		&c.ETag, &c.ModifiedBy, &c.DeletedAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, model.ErrNotFound
	}
	return c, err
}

// ListContactsInBooks returns live contacts tagged in at least one of the
// given books, ordered by filename. Empty book set means no contacts (everything is
// invisible).
func (s ScopedStore) ListContactsInBooks(ctx context.Context, bookIDs []int64) ([]Contact, error) {
	if len(bookIDs) == 0 {
		return []Contact{}, nil
	}
	rows, err := s.Tx.Query(ctx, `
		SELECT DISTINCT c.id, c.filename, c.vcard_text, c.uid, c.search_meta,
		       c.etag, c.modified_by, c.deleted_at, c.created_at, c.updated_at
		  FROM contacts c
		  JOIN contact_books cb ON cb.contact_id = c.id
		 WHERE c.user_id = $1 AND c.deleted_at IS NULL AND cb.book_id = ANY($2)
		 ORDER BY c.filename`, s.Actor.UserID, bookIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := make([]Contact, 0)
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// ContactBookIDs returns the books a contact is tagged in, verified to be the
// actor's own contact.
func (s ScopedStore) ContactBookIDs(ctx context.Context, contactID int64) ([]int64, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT cb.book_id
		  FROM contact_books cb
		  JOIN contacts c ON c.id = cb.contact_id
		 WHERE c.id = $1 AND c.user_id = $2`, contactID, s.Actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// LiveContactByFilename returns one live contact by filename, or ErrNotFound
// on a miss.
func (s ScopedStore) LiveContactByFilename(ctx context.Context, filename string) (Contact, error) {
	return scanContact(s.Tx.QueryRow(ctx, `
		SELECT id, filename, vcard_text, uid, search_meta, etag,
		       modified_by, deleted_at, created_at, updated_at
		  FROM contacts
		 WHERE user_id = $1 AND filename = $2 AND deleted_at IS NULL`,
		s.Actor.UserID, filename))
}

// PutContact handles preconditions, live-UID dedupe, default system-book
// tagging on create, and a contact_changes 'put' row. Failure paths return
// ErrPrecondition (412), ErrNotFound (404), ErrUIDConflict or ErrFilenameRetired
// (409). Caller must hold the sync lock.
func (s ScopedStore) PutContact(ctx context.Context, p PutContactParams, cond Precondition) (PutResult, error) {
	text := normalizeLineEndings(p.VCardText)
	etag := sha256Hex(text)

	if cond.IfMatch != nil {
		// If-Match: force-update only a row whose etag matches, else 412/404.
		var gate *string
		if *cond.IfMatch != "*" {
			gate = cond.IfMatch
		}
		res, err := s.updateContact(ctx, p, text, etag, gate)
		if err != nil {
			return PutResult{}, err
		}
		if res.Created {
			return s.afterPutChange(ctx, p.Filename, res)
		}
		return PutResult{}, s.ifMatchMiss(ctx, p.Filename)
	}

	if cond.IfNoneMatchAll {
		// If-None-Match:*: create-only. An existing live resource returns 412.
		created, err := s.insertContact(ctx, p, text, etag)
		if err != nil {
			return PutResult{}, err
		}
		if created {
			return s.afterPutChange(ctx, p.Filename, PutResult{Created: true, ETag: etag})
		}
		return PutResult{}, model.ErrPrecondition
	}

	// No preconditions: overwrite-if-exists, else insert.
	res, err := s.updateContact(ctx, p, text, etag, nil)
	if err != nil {
		return PutResult{}, err
	}
	if res.Created {
		return s.afterPutChange(ctx, p.Filename, res)
	}
	created, err := s.insertContact(ctx, p, text, etag)
	if err != nil {
		return PutResult{}, err
	}
	if created {
		return s.afterPutChange(ctx, p.Filename, PutResult{Created: true, ETag: etag})
	}
	return PutResult{}, model.ErrFilenameRetired
}

// updateContact overwrites a live row if one exists. gate, when non-nil, is an
// etag the existing row must match. Reports Created=false when nothing matched
// (whether absent or tombstoned); Created=true on a real update.
func (s ScopedStore) updateContact(ctx context.Context, p PutContactParams, text, etag string, gate *string) (PutResult, error) {
	q := `
		UPDATE contacts
		   SET vcard_text = $3, uid = $4, search_meta = $5,
		       etag = $6, modified_by = $1, updated_at = now()
		 WHERE user_id = $1 AND filename = $2 AND deleted_at IS NULL`
	args := []any{s.Actor.UserID, p.Filename, text, p.UID, p.SearchMeta, etag}
	if gate != nil {
		q += ` AND etag = $7`
		args = append(args, *gate)
	}
	q += ` RETURNING id`
	var id int64
	err := s.Tx.QueryRow(ctx, q, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return PutResult{}, nil
	}
	if isLiveUIDConflict(err) {
		return PutResult{}, mapUIDConflict(ctx, s, p.UID)
	}
	if err != nil {
		return PutResult{}, err
	}
	return PutResult{Created: true, ETag: etag}, nil
}

func (s ScopedStore) afterPutChange(ctx context.Context, filename string, res PutResult) (PutResult, error) {
	if err := AppendChange(ctx, s.Tx, s.Actor.UserID, filename, "put"); err != nil {
		return PutResult{}, err
	}
	return res, nil
}

// ifMatchMiss resolves an If-Match update that matched zero rows: a live
// contact that exists but failed the etag predicate returns 412; anything
// else returns 404.
func (s ScopedStore) ifMatchMiss(ctx context.Context, filename string) error {
	var deletedAt *time.Time
	err := s.Tx.QueryRow(ctx, `
		SELECT deleted_at FROM contacts
		 WHERE user_id = $1 AND filename = $2`,
		s.Actor.UserID, filename).Scan(&deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	if err != nil {
		return err
	}
	if deletedAt == nil {
		return model.ErrPrecondition
	}
	return model.ErrNotFound
}

// insertContact runs the INSERT used by both the create-if-absent and the
// no-precondition fallthrough paths. Reports whether a row was inserted.
func (s ScopedStore) insertContact(ctx context.Context, p PutContactParams, text, etag string) (bool, error) {
	var id int64
	err := s.Tx.QueryRow(ctx, `
		INSERT INTO contacts (user_id, filename, vcard_text, uid, search_meta, etag, modified_by)
		VALUES ($1, $2, $3, $4, $5, $6, $1)
		ON CONFLICT (user_id, filename) DO NOTHING
		RETURNING id`,
		s.Actor.UserID, p.Filename, text, p.UID, p.SearchMeta, etag,
	).Scan(&id)
	if err == nil {
		if err := s.tagSystemBook(ctx, id); err != nil {
			return false, err
		}
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if isLiveUIDConflict(err) {
		return false, mapUIDConflict(ctx, s, p.UID)
	}
	return false, err
}

func (s ScopedStore) tagSystemBook(ctx context.Context, contactID int64) error {
	var bookID int64
	if err := s.Tx.QueryRow(ctx, `
		SELECT id FROM books WHERE owner_user_id = $1 AND is_system`,
		s.Actor.UserID).Scan(&bookID); err != nil {
		return err
	}
	_, err := s.Tx.Exec(ctx, `
		INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING`, contactID, bookID)
	return err
}

// DeleteContact soft-deletes a contact and appends a 'delete' change.
// Returns false + nil when the named contact does not exist.
func (s ScopedStore) DeleteContact(ctx context.Context, filename string) (bool, error) {
	tag, err := s.Tx.Exec(ctx, `
		UPDATE contacts
		   SET deleted_at = now()
		 WHERE user_id = $1 AND filename = $2 AND deleted_at IS NULL`,
		s.Actor.UserID, filename)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := AppendChange(ctx, s.Tx, s.Actor.UserID, filename, "delete"); err != nil {
		return false, err
	}
	return true, nil
}

// PurgeTombstones hard-deletes contacts tombstoned before the cutoff, in
// batches of at most limit. Returns how many rows were deleted.
func PurgeTombstones(ctx context.Context, pool *pgxpool.Pool, before time.Time, limit int) (int64, error) {
	var n int64
	err := WithTx(ctx, pool, model.Actor{}, func(s ScopedStore) error {
		var err error
		n, err = s.Prune(ctx, before, limit)
		return err
	})
	return n, err
}

// Prune hard-deletes contacts tombstoned before the given cutoff.
func (s ScopedStore) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT id FROM contacts
		 WHERE deleted_at IS NOT NULL AND deleted_at < $1
		 ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`, before, limit)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, 64)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.Tx.Exec(ctx, `DELETE FROM contacts WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func normalizeLineEndings(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func isLiveUIDConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idx_contacts_live_uid"
}

func mapUIDConflict(ctx context.Context, s ScopedStore, uid string) error {
	var filename string
	if err := s.Tx.QueryRow(ctx, `
		SELECT filename FROM contacts
		 WHERE user_id = $1 AND uid = $2 AND deleted_at IS NULL`,
		s.Actor.UserID, uid).Scan(&filename); err != nil {
		return model.ErrUIDConflict
	}
	return &UIDConflictError{Filename: filename}
}

// UIDConflictError names the live contact already holding the UID (409).
type UIDConflictError struct {
	Filename string
}

func (e *UIDConflictError) Error() string {
	return "live UID conflict with " + e.Filename
}

func (e *UIDConflictError) Unwrap() error { return model.ErrUIDConflict }
