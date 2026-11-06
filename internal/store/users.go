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
		  WHERE username = $1 OR lower(email) = lower($1)`, identifier,
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

// NeedsSetup reports whether the server has no accounts yet.
func (u *Users) NeedsSetup(ctx context.Context) (bool, error) {
	var exists bool
	err := u.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists)
	return !exists, err
}

// firstAccountLock serializes concurrent setup requests, so only one account can
// win the empty-table race.
const firstAccountLock = int64(0x6d6e656d6f)

// RegisterFirst creates the first account and rejects the call once any user
// exists. Unknown state is model.ErrConflict.
func (u *Users) RegisterFirst(ctx context.Context, username, email, name, password string, bcryptCost int) (User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return User{}, err
	}

	var user User
	err = pgx.BeginFunc(ctx, u.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, firstAccountLock); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return model.ErrConflict
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO users (username, email, name, password_hash)
			VALUES ($1, $2, $3, $4)
			RETURNING id, username, email, name, password_hash, created_at`,
			username, email, name, string(hash),
		).Scan(&user.ID, &user.Username, &user.Email, &user.Name, &user.PasswordHash, &user.CreatedAt)
		if err != nil {
			return mapSignupError(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_settings (user_id) VALUES ($1)`, user.ID)
		return err
	})
	return user, err
}

func CheckPassword(user User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
}

// dummyHash gives the not-found path the same cost as a real comparison, so
// response time does not reveal whether a login exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("mnemo-dummy-password"), 12)

// Authenticate verifies a login and password pair. Accounts are matched by
// username or email, case-insensitively for the email.
func (u *Users) Authenticate(ctx context.Context, login, password string) (User, bool) {
	user, err := u.ByLogin(ctx, login)
	if err != nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return User{}, false
	}
	if !CheckPassword(user, password) {
		return User{}, false
	}
	return user, true
}

// SetPassword stores a new password hash for the account matching login.
// Unknown login is model.ErrNotFound.
func (u *Users) SetPassword(ctx context.Context, login, password string, bcryptCost int) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return err
	}
	tag, err := u.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2 WHERE username = $1 OR lower(email) = lower($1)`,
		login, string(hash))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, model.ErrNotFound
	}
	return u, err
}
