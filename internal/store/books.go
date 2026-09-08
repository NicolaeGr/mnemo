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

// Signup creates a user + settings + system book in one tx (§5.1).
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
// the resolver is built on, §6.1).
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

func scanBook(row pgx.Row) (Book, error) {
	var b Book
	err := row.Scan(&b.ID, &b.OwnerUserID, &b.Slug, &b.DisplayName, &b.Description,
		&b.SortOrder, &b.IsActive, &b.IsSystem, &b.SyncedTiers, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Book{}, model.ErrNotFound
	}
	return b, err
}
