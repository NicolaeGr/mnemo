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

const (
	// maxAttempts failures before an event is dead-lettered and left alone.
	maxAttempts = 5
	// backoffCap bounds the exponential retry delay.
	backoffCap = time.Hour
)

// ClaimEvents claims a batch of pending events that are due (not yet dispatched,
// not dead-lettered, past any backoff): FOR UPDATE SKIP LOCKED.
func ClaimEvents(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Event, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, target_user_id, kind, payload, initiated_by, created_at, dispatched_at
		  FROM user_events
		 WHERE dispatched_at IS NULL
		   AND NOT dead_letter
		   AND next_attempt_at <= now()
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
// stays pending; the failure is then recorded out-of-band so the event backs
// off and eventually dead-letters instead of retrying every poll.
func ApplyEvent(ctx context.Context, pool *pgxpool.Pool, e Event) error {
	actor := model.Actor{UserID: e.TargetUserID}
	err := WithTx(ctx, pool, actor, func(s ScopedStore) error {
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
	if err != nil {
		_ = RecordFailure(ctx, pool, e.ID)
	}
	return err
}

// RecordFailure advances an event's retry bookkeeping after a failed apply: it
// backs off exponentially (1s, 2s, …) and, past maxAttempts, dead-letters the
// event so the poller stops claiming it. Errors here are ignored by callers:
// the worst case is an event retrying one poll early.
func RecordFailure(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM user_events WHERE id = $1`, id).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	if attempts >= maxAttempts {
		_, err := pool.Exec(ctx, `
			UPDATE user_events SET attempts = $2, dead_letter = true
			 WHERE id = $1 AND dispatched_at IS NULL`, id, attempts)
		return err
	}
	delay := time.Second << (attempts - 1)
	if delay > backoffCap {
		delay = backoffCap
	}
	_, err := pool.Exec(ctx, `
		UPDATE user_events SET attempts = $2, next_attempt_at = now() + $3::interval
		 WHERE id = $1 AND dispatched_at IS NULL`, id, attempts, delay.String())
	return err
}

// PendingEventCount reports outbox backlog depth (watchdog input). Dead-lettered
// events are excluded; they are the caller's to inspect, not a backlog to alert
// on.
func PendingEventCount(ctx context.Context, pool *pgxpool.Pool, olderThan time.Duration) (int64, error) {
	var n int64
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM user_events
		 WHERE dispatched_at IS NULL
		   AND NOT dead_letter
		   AND created_at < now() - $1::interval`,
		olderThan.String()).Scan(&n)
	return n, err
}
