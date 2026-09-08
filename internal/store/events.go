package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// EnqueueEvent inserts a pending event for a target user (Part-10 discipline).
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

// ClaimEvents claims a batch of pending events for a target partition:
// FOR UPDATE SKIP LOCKED in autocommit. Returns claimed rows.
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

// MarkEventDispatched stamps an event dispatched.
func MarkEventDispatched(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE user_events SET dispatched_at = now() WHERE id = $1`, id)
	return err
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
