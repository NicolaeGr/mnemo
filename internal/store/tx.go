package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/model"
)

// ScopedStore carries a live transaction plus the acting principal so every
// repository method can apply user_id (and, where relevant, tier) scoping in
// its SQL without passing those values as arguments (C2.2).
type ScopedStore struct {
	Tx    pgx.Tx
	Actor model.Actor
}

// WithTx runs fn inside a single transaction (C2.1). On error the tx is rolled
// back and the error is returned unwrapped.
func WithTx(ctx context.Context, pool *pgxpool.Pool, actor model.Actor, fn func(ScopedStore) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	s := ScopedStore{Tx: tx, Actor: actor}
	if err := fn(s); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}

// LockSync serializes sync-relevant writes per user (C2.3), so commit order of
// contact_changes equals id-allocation order within a user (I4).
func LockSync(ctx context.Context, tx pgx.Tx, userID int64) error {
	_, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('sync:' || $1::bigint::text, 0))`, userID)
	return err
}
