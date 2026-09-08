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
