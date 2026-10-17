package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/nicolaegr/mnemo/internal/model"
)

type Book struct {
	ID          int64
	OwnerUserID int64
	Slug        string
	DisplayName string
	Description *string
	SortOrder   int
	IsActive    bool
	SyncedTiers []string
	CreatedAt   time.Time
}

// Signup creates a user and an empty settings row in one tx.
func (u *Users) Signup(ctx context.Context, username, email, name, password string, bcryptCost int) (User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return User{}, err
	}

	var user User
	err = pgx.BeginFunc(ctx, u.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO users (username, email, name, password_hash)
			VALUES ($1, $2, $3, $4)
			RETURNING id, username, email, name, password_hash, created_at`,
			username, email, name, string(hash),
		).Scan(&user.ID, &user.Username, &user.Email, &user.Name, &user.PasswordHash, &user.CreatedAt)
		if err != nil {
			return mapSignupError(err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO user_settings (user_id) VALUES ($1)`, user.ID); err != nil {
			return err
		}
		return nil
	})
	return user, err
}

func mapSignupError(err error) error {
	if err == nil {
		return nil
	}
	pgErr, ok := err.(*pgconn.PgError)
	if !ok {
		return err
	}
	switch pgErr.Code {
	case "23505":
		switch pgErr.ConstraintName {
		case "users_username_key":
			return model.ErrUsernameTaken
		case "users_email_key":
			return model.ErrEmailTaken
		}
	case "23514":
		// A check-constraint failure is bad input, not a server fault.
		return model.ErrPrecondition
	}
	return err
}

// BookSlugTaken reports whether the actor already owns a book with this slug, so
// the add form can warn before the unique constraint fires.
func (s ScopedStore) BookSlugTaken(ctx context.Context, slug string) (bool, error) {
	var taken bool
	err := s.Tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM books WHERE owner_user_id = $1 AND slug = $2)`,
		s.Actor.UserID, slug).Scan(&taken)
	return taken, err
}

// ActiveBooks lists the actor's active books within the current tx (the read
// the resolver is built on).
func (s ScopedStore) ActiveBooks(ctx context.Context) ([]Book, error) {
	return s.queryBooks(ctx, `WHERE owner_user_id = $1 AND is_active`)
}

// ListBooks lists all of the actor's books, active or not, for management views
// that must be able to reactivate a hidden book.
func (s ScopedStore) ListBooks(ctx context.Context) ([]Book, error) {
	return s.queryBooks(ctx, `WHERE owner_user_id = $1`)
}

// GetBook returns one owned book, or ErrNotFound.
func (s ScopedStore) GetBook(ctx context.Context, bookID int64) (Book, error) {
	return scanBook(s.Tx.QueryRow(ctx, `
		SELECT id, owner_user_id, slug, display_name, description, sort_order,
		       is_active, synced_tiers::text[], created_at
		  FROM books
		 WHERE owner_user_id = $1 AND id = $2`, s.Actor.UserID, bookID))
}

func (s ScopedStore) queryBooks(ctx context.Context, where string) ([]Book, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT id, owner_user_id, slug, display_name, description, sort_order,
		       is_active, synced_tiers::text[], created_at
		  FROM books
		 `+where+`
		 ORDER BY sort_order, slug`, s.Actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	books := make([]Book, 0)
	for rows.Next() {
		book, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		books = append(books, book)
	}
	return books, rows.Err()
}

// CreateBook adds a new active book (default synced_tiers) for the actor. A
// duplicate slug is ErrConflict. A new empty book changes nothing a principal
// syncs, so there is no epoch bump.
func (s ScopedStore) CreateBook(ctx context.Context, slug, displayName string, description *string, sortOrder int) (Book, error) {
	var b Book
	err := s.Tx.QueryRow(ctx, `
		INSERT INTO books (owner_user_id, slug, display_name, description, sort_order)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, owner_user_id, slug, display_name, description, sort_order,
		          is_active, synced_tiers::text[], created_at`,
		s.Actor.UserID, slug, displayName, description, sortOrder,
	).Scan(&b.ID, &b.OwnerUserID, &b.Slug, &b.DisplayName, &b.Description,
		&b.SortOrder, &b.IsActive, &b.SyncedTiers, &b.CreatedAt)
	if isUniqueViolation(err) {
		return Book{}, model.ErrConflict
	}
	return b, err
}

// EnsureBook returns the actor's book whose display name matches name, ignoring
// case, creating it when absent. A created book is active and carries the given
// approval flag, so a user import can approve lists while a DAV-triggered import
// leaves them unapproved. Caller holds LockSync so concurrent imports cannot race
// for the same slug.
func (s ScopedStore) EnsureBook(ctx context.Context, name string, approved bool) (Book, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Book{}, false, model.ErrPrecondition
	}

	b, err := scanBook(s.Tx.QueryRow(ctx, `
		SELECT id, owner_user_id, slug, display_name, description, sort_order,
		       is_active, synced_tiers::text[], created_at
		  FROM books
		 WHERE owner_user_id = $1 AND lower(display_name) = lower($2)
		 ORDER BY id
		 LIMIT 1`, s.Actor.UserID, name))
	if err == nil {
		return b, false, nil
	}
	if !errors.Is(err, model.ErrNotFound) {
		return Book{}, false, err
	}

	base := bookSlug(name)
	for n := 1; n <= 1000; n++ {
		slug := base
		if n > 1 {
			slug = base + "-" + strconv.Itoa(n)
		}
		var taken bool
		if err := s.Tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM books WHERE owner_user_id = $1 AND slug = $2)`,
			s.Actor.UserID, slug).Scan(&taken); err != nil {
			return Book{}, false, err
		}
		if taken {
			continue
		}
		b, err = scanBook(s.Tx.QueryRow(ctx, `
			INSERT INTO books (owner_user_id, slug, display_name, is_approved)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (owner_user_id, slug) DO NOTHING
			RETURNING id, owner_user_id, slug, display_name, description, sort_order,
			          is_active, synced_tiers::text[], created_at`,
			s.Actor.UserID, slug, name, approved))
		if errors.Is(err, model.ErrNotFound) {
			continue
		}
		if err != nil {
			return Book{}, false, err
		}
		return b, true, nil
	}
	return Book{}, false, model.ErrConflict
}

// bookSlug reduces a display name to a valid book slug: lowercase letters and
// digits, with every other run collapsed to one hyphen.
func bookSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "list"
	}
	return slug
}

// SetBookActive activates/deactivates a book. Flipping is_active changes the
// union every principal syncs, so it bumps them all; no-op if the value is
// unchanged. Caller holds LockSync.
func (s ScopedStore) SetBookActive(ctx context.Context, bookID int64, active bool) error {
	if err := s.requireOwnedBook(ctx, bookID); err != nil {
		return err
	}
	tag, err := s.Tx.Exec(ctx, `
		UPDATE books SET is_active = $3
		 WHERE owner_user_id = $1 AND id = $2 AND is_active <> $3`,
		s.Actor.UserID, bookID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return s.bumpAllPrincipals(ctx)
	}
	return nil
}

// SetBookTiers replaces a book's synced_tiers; a real change bumps all of the
// actor's principals. A set must be non-empty with archived kept exclusive.
// Caller holds LockSync.
func (s ScopedStore) SetBookTiers(ctx context.Context, bookID int64, tiers []string) error {
	if err := s.requireOwnedBook(ctx, bookID); err != nil {
		return err
	}
	if err := validTiers(tiers); err != nil {
		return err
	}
	tag, err := s.Tx.Exec(ctx, `
		UPDATE books SET synced_tiers = $3::principal_tier[]
		 WHERE owner_user_id = $1 AND id = $2 AND synced_tiers <> $3::principal_tier[]`,
		s.Actor.UserID, bookID, tiers)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return s.bumpAllPrincipals(ctx)
	}
	return nil
}

// validTiers rejects an empty set and one mixing archived with the live tiers.
func validTiers(tiers []string) error {
	if len(tiers) == 0 {
		return model.ErrInvalidTiers
	}
	var archived, live bool
	for _, t := range tiers {
		switch t {
		case "archived":
			archived = true
		case "primary", "secondary":
			live = true
		default:
			return model.ErrInvalidTiers
		}
	}
	if archived && live {
		return model.ErrInvalidTiers
	}
	return nil
}

// DeleteBook removes a book. Contacts tagged only in it become untagged, and
// their CATEGORIES is rewritten. Then it bumps all principals. Caller holds
// LockSync.
func (s ScopedStore) DeleteBook(ctx context.Context, bookID int64) error {
	if err := s.requireOwnedBook(ctx, bookID); err != nil {
		return err
	}

	files, err := s.filenamesInBook(ctx, bookID)
	if err != nil {
		return err
	}

	if _, err := s.Tx.Exec(ctx, `DELETE FROM contact_books WHERE book_id = $1`, bookID); err != nil {
		return err
	}
	if _, err := s.Tx.Exec(ctx, `DELETE FROM books WHERE id = $1`, bookID); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := s.applyCategories(ctx, f, false); err != nil {
			return err
		}
	}
	return s.bumpAllPrincipals(ctx)
}

func (s ScopedStore) requireOwnedBook(ctx context.Context, bookID int64) error {
	var one int
	err := s.Tx.QueryRow(ctx,
		`SELECT 1 FROM books WHERE owner_user_id = $1 AND id = $2`,
		s.Actor.UserID, bookID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return err
}

func (s ScopedStore) bumpAllPrincipals(ctx context.Context) error {
	ids, err := s.principalIDs(ctx)
	if err != nil {
		return err
	}
	_, err = BumpPrincipalsEpoch(ctx, s.Tx, ids)
	return err
}

func (s ScopedStore) principalIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.Tx.Query(ctx, `SELECT id FROM principals WHERE user_id = $1`, s.Actor.UserID)
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

// RenameBook updates a book's display name and rewrites the CATEGORIES of every
// contact tagged in it, since group names follow book names. A rename does not
// change what a principal syncs, so there is no epoch bump.
func (s ScopedStore) RenameBook(ctx context.Context, bookID int64, displayName string) error {
	if err := s.requireOwnedBook(ctx, bookID); err != nil {
		return err
	}
	if _, err := s.Tx.Exec(ctx, `
		UPDATE books SET display_name = $3
		 WHERE owner_user_id = $1 AND id = $2`,
		s.Actor.UserID, bookID, displayName); err != nil {
		return err
	}
	return s.refreshBookCategories(ctx, bookID)
}

// SetBookDescription updates a book's description. Device-invisible, so no epoch
// bump.
func (s ScopedStore) SetBookDescription(ctx context.Context, bookID int64, description *string) error {
	if err := s.requireOwnedBook(ctx, bookID); err != nil {
		return err
	}
	_, err := s.Tx.Exec(ctx, `
		UPDATE books SET description = $3
		 WHERE owner_user_id = $1 AND id = $2`,
		s.Actor.UserID, bookID, description)
	return err
}

// ReorderBooks sets sort_order by position for the submitted ids. Ids not owned
// by the actor are skipped. Device-invisible, so no epoch bump.
func (s ScopedStore) ReorderBooks(ctx context.Context, ordered []int64) error {
	for i, id := range ordered {
		if _, err := s.Tx.Exec(ctx, `
			UPDATE books SET sort_order = $1
			 WHERE owner_user_id = $2 AND id = $3`,
			i*100, s.Actor.UserID, id); err != nil {
			return err
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// ContactPreview is the handful of fields a list card shows for one tagged
// contact, read straight from search_meta to avoid decoding every vCard.
type ContactPreview struct {
	Name  string
	Tel   string
	Email string
}

// BookSummary is one book with the numbers the management view needs: how many
// live contacts it holds and a short preview of them.
type BookSummary struct {
	Book         Book
	ContactCount int
	Preview      []ContactPreview
}

// ListBookSummaries lists the actor's books, each with its live-contact count
// and up to previewLimit preview contacts (LIFO of the name ordering). Books
// with no contacts still appear, with a zero count and an empty preview.
func (s ScopedStore) ListBookSummaries(ctx context.Context, previewLimit int) ([]BookSummary, error) {
	books, err := s.ListBooks(ctx)
	if err != nil {
		return nil, err
	}
	if previewLimit <= 0 {
		previewLimit = 6
	}
	summaries := make([]BookSummary, len(books))
	index := make(map[int64]int, len(books))
	for i, b := range books {
		summaries[i] = BookSummary{Book: b, Preview: []ContactPreview{}}
		index[b.ID] = i
	}

	rows, err := s.Tx.Query(ctx, `
		SELECT t.book_id, t.total, t.fn, t.tel, t.email
		  FROM (
			SELECT cb.book_id,
			       count(*) OVER (PARTITION BY cb.book_id) AS total,
			       row_number() OVER (PARTITION BY cb.book_id
			                          ORDER BY lower(coalesce(c.search_meta->>'fn', '')), c.filename) AS rn,
			       coalesce(c.search_meta->>'fn', c.uid) AS fn,
			       c.search_meta->'tels'->0->>'raw' AS tel,
			       c.search_meta->'emails'->>0 AS email
			  FROM contact_books cb
			  JOIN contacts c ON c.id = cb.contact_id
			 WHERE c.user_id = $1 AND c.deleted_at IS NULL
		) t
		 WHERE t.rn <= $2
		 ORDER BY t.book_id, t.rn`, s.Actor.UserID, previewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var bookID int64
		var total int
		var fn, tel, email *string
		if err := rows.Scan(&bookID, &total, &fn, &tel, &email); err != nil {
			return nil, err
		}
		i, ok := index[bookID]
		if !ok {
			continue
		}
		summaries[i].ContactCount = total
		summaries[i].Preview = append(summaries[i].Preview, ContactPreview{
			Name: deref(fn), Tel: deref(tel), Email: deref(email),
		})
	}
	return summaries, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func scanBook(row pgx.Row) (Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.OwnerUserID, &b.Slug, &b.DisplayName, &b.Description,
		&b.SortOrder, &b.IsActive, &b.SyncedTiers, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Book{}, model.ErrNotFound
	}
	return b, err
}
