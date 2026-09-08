package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"example.com/segments/internal/model"
)

type Book struct {
	ID          int64
	OwnerUserID int64
	Slug        string
	DisplayName string
	Description *string
	SortOrder   int
	IsActive    bool
	IsSystem    bool
	SyncedTiers []string
	CreatedAt   time.Time
}

// Signup creates a user + settings + system book in one tx.
func (u *Users) Signup(ctx context.Context, username, email, name, password string, bcryptCost int) (User, Book, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return User{}, Book{}, err
	}

	var user User
	var book Book
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

		err = tx.QueryRow(ctx, `
			INSERT INTO books (owner_user_id, slug, display_name, sort_order, is_system)
			VALUES ($1, 'all', 'Default Contacts', 0, true)
			RETURNING id, owner_user_id, slug, display_name, description, sort_order,
			          is_active, is_system, synced_tiers::text[], created_at`,
			user.ID,
		).Scan(&book.ID, &book.OwnerUserID, &book.Slug, &book.DisplayName, &book.Description,
			&book.SortOrder, &book.IsActive, &book.IsSystem, &book.SyncedTiers, &book.CreatedAt)
		if err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO user_settings (user_id, default_book_id)
			VALUES ($1, $2)`, user.ID, book.ID); err != nil {
			return err
		}
		return nil
	})
	return user, book, err
}

func mapSignupError(err error) error {
	if err == nil {
		return nil
	}
	if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case "users_username_key":
			return model.ErrUsernameTaken
		case "users_email_key":
			return model.ErrEmailTaken
		}
	}
	return err
}

// ActiveBooks lists the actor's active books within the current tx (the read
// the resolver is built on).
func (s ScopedStore) ActiveBooks(ctx context.Context) ([]Book, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT id, owner_user_id, slug, display_name, description, sort_order,
		       is_active, is_system, synced_tiers::text[], created_at
		  FROM books
		 WHERE owner_user_id = $1 AND is_active
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
		          is_active, is_system, synced_tiers::text[], created_at`,
		s.Actor.UserID, slug, displayName, description, sortOrder,
	).Scan(&b.ID, &b.OwnerUserID, &b.Slug, &b.DisplayName, &b.Description,
		&b.SortOrder, &b.IsActive, &b.IsSystem, &b.SyncedTiers, &b.CreatedAt)
	if isUniqueViolation(err) {
		return Book{}, model.ErrConflict
	}
	return b, err
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
// actor's principals. Caller holds LockSync.
func (s ScopedStore) SetBookTiers(ctx context.Context, bookID int64, tiers []string) error {
	if err := s.requireOwnedBook(ctx, bookID); err != nil {
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

// DeleteBook removes a non-system book, folding its tags onto the system book
// first so every contact keeps a tag, then bumps all principals. System-book
// deletion is ErrConflict. Caller holds LockSync.
func (s ScopedStore) DeleteBook(ctx context.Context, bookID int64) error {
	var isSystem bool
	if err := s.Tx.QueryRow(ctx, `
		SELECT is_system FROM books WHERE owner_user_id = $1 AND id = $2`,
		s.Actor.UserID, bookID).Scan(&isSystem); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return model.ErrNotFound
		}
		return err
	}
	if isSystem {
		return model.ErrConflict
	}

	var systemID int64
	if err := s.Tx.QueryRow(ctx,
		`SELECT id FROM books WHERE owner_user_id = $1 AND is_system`, s.Actor.UserID,
	).Scan(&systemID); err != nil {
		return err
	}
	if _, err := s.Tx.Exec(ctx, `
		INSERT INTO contact_books (contact_id, book_id)
		SELECT contact_id, $1 FROM contact_books WHERE book_id = $2
		ON CONFLICT DO NOTHING`, systemID, bookID); err != nil {
		return err
	}
	if _, err := s.Tx.Exec(ctx, `DELETE FROM contact_books WHERE book_id = $1`, bookID); err != nil {
		return err
	}
	if _, err := s.Tx.Exec(ctx, `DELETE FROM books WHERE id = $1`, bookID); err != nil {
		return err
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

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func scanBook(row pgx.Row) (Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.OwnerUserID, &b.Slug, &b.DisplayName, &b.Description,
		&b.SortOrder, &b.IsActive, &b.IsSystem, &b.SyncedTiers, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Book{}, model.ErrNotFound
	}
	return b, err
}
