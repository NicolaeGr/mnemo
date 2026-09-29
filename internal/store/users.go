package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/nicolaegr/mnemo/internal/model"
)

type User struct {
	ID           int64
	Username     string
	Email        string
	Name         string
	PasswordHash string
	CreatedAt    time.Time
}

type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

func (u *Users) ByLogin(ctx context.Context, identifier string) (User, error) {
	row := u.pool.QueryRow(ctx,
		`SELECT id, username, email, name, password_hash, created_at
		   FROM users
		  WHERE username = $1 OR email ILIKE $1`, identifier,
	)
	return scanUser(row)
}

func (u *Users) ByEmail(ctx context.Context, email string) (User, error) {
	row := u.pool.QueryRow(ctx,
		`SELECT id, username, email, name, password_hash, created_at FROM users WHERE email=$1`, email,
	)
	return scanUser(row)
}

func (u *Users) ByID(ctx context.Context, id int64) (User, error) {
	row := u.pool.QueryRow(ctx,
		`SELECT id, username, email, name, password_hash, created_at FROM users WHERE id=$1`, id,
	)
	return scanUser(row)
}

// SetDefaultBook points the user's default book at an owned, active book. A
// missing or foreign book is ErrNotFound.
func (u *Users) SetDefaultBook(ctx context.Context, userID, bookID int64) error {
	var one int
	err := u.pool.QueryRow(ctx, `
		SELECT 1 FROM books WHERE owner_user_id = $1 AND id = $2 AND is_active`,
		userID, bookID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = u.pool.Exec(ctx, `
		INSERT INTO user_settings (user_id, default_book_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET default_book_id = $2, updated_at = now()`,
		userID, bookID)
	return err
}

// DefaultBook returns the user's default book id, or nil when unset.
func (u *Users) DefaultBook(ctx context.Context, userID int64) (*int64, error) {
	var id *int64
	err := u.pool.QueryRow(ctx,
		`SELECT default_book_id FROM user_settings WHERE user_id = $1`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return id, err
}

func CheckPassword(user User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, model.ErrNotFound
	}
	return u, err
}
