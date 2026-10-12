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
	// leaseDuration is how long a claimed event stays unavailable to other
	// dispatcher workers. It must exceed the time to apply one event (seconds);
	// it only needs to elapse on its own if a worker dies mid-claim.
	leaseDuration = 30 * time.Second
)

// ClaimEvents claims a batch of due, unclaimed events in a single atomic
// statement: the same statement selects candidates FOR UPDATE SKIP LOCKED and
// stamps leased_until, so concurrent workers cannot both claim a row. A claimed
// event is unavailable until it is dispatched, its lease expires, or a failure
// clears the lease. The lease is what makes this safe with N workers.
func ClaimEvents(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Event, error) {
	rows, err := pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM user_events
			 WHERE dispatched_at IS NULL
			   AND NOT dead_letter
			   AND next_attempt_at <= now()
			   AND (leased_until IS NULL OR leased_until < now())
			 ORDER BY id
			 LIMIT $1
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE user_events e
		   SET leased_until = now() + $2::interval
		  FROM due
		 WHERE e.id = due.id
		RETURNING e.id, e.target_user_id, e.kind, e.payload, e.initiated_by,
		          e.created_at, e.dispatched_at`, limit, leaseDuration.String())
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
			UPDATE user_events SET dispatched_at = now(), leased_until = NULL
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
			UPDATE user_events SET attempts = $2, dead_letter = true, leased_until = NULL
			 WHERE id = $1 AND dispatched_at IS NULL`, id, attempts)
		return err
	}
	delay := time.Second << (attempts - 1)
	if delay > backoffCap {
		delay = backoffCap
	}
	_, err := pool.Exec(ctx, `
		UPDATE user_events SET attempts = $2, next_attempt_at = now() + $3::interval, leased_until = NULL
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
