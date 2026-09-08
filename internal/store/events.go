package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
)

// Event is one user_events outbox row.
type Event struct {
	ID           int64
	TargetUserID int64
	Kind         string
	Payload      []byte
	InitiatedBy  *int64
	CreatedAt    time.Time
	DispatchedAt *time.Time
}

// EnqueueEvent inserts a pending event for a target user.
func EnqueueEvent(ctx context.Context, pool *pgxpool.Pool, targetUserID int64, kind string, payload any, initiatedBy *int64) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO user_events (target_user_id, kind, payload, initiated_by)
		VALUES ($1, $2, $3, $4)`, targetUserID, kind, raw, initiatedBy)
	return err
}

// ClaimEvents claims a batch of pending events: FOR UPDATE SKIP LOCKED.
func ClaimEvents(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Event, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, target_user_id, kind, payload, initiated_by, created_at, dispatched_at
		  FROM user_events
		 WHERE dispatched_at IS NULL
		 ORDER BY id
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Event, 0)
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TargetUserID, &e.Kind, &e.Payload,
			&e.InitiatedBy, &e.CreatedAt, &e.DispatchedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ApplyEvent runs one event's handler for its target user and marks it
// dispatched in the same transaction. An error rolls back both, so the event
// stays pending for the next poll.
func ApplyEvent(ctx context.Context, pool *pgxpool.Pool, e Event) error {
	actor := model.Actor{UserID: e.TargetUserID}
	return WithTx(ctx, pool, actor, func(s ScopedStore) error {
		if err := LockSync(ctx, s.Tx, e.TargetUserID); err != nil {
			return err
		}
		switch e.Kind {
		case "force_resync":
			if err := s.bumpAllPrincipals(ctx); err != nil {
				return err
			}
		default:
			return fmt.Errorf("dispatch: unknown event kind %q", e.Kind)
		}
		_, err := s.Tx.Exec(ctx, `
			UPDATE user_events SET dispatched_at = now()
			 WHERE id = $1 AND dispatched_at IS NULL`, e.ID)
		return err
	})
}

// PendingEventCount reports outbox backlog depth (watchdog input).
func PendingEventCount(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_events
		 WHERE dispatched_at IS NULL AND created_at < now() - $1::interval`,
		olderThan.String()).Scan(&n)
	return n, err
}
