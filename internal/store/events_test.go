package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nicolaegr/mnemo/internal/store"
)

func TestForceResyncEvent(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()

	user, _, err := store.NewUsers(pool).Signup(ctx, "eventuser", "event@example.com", "Event", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	principal := insertPrincipal(t, ctx, pool, user.ID, "primary")
	before := epochOf(t, ctx, pool, principal)

	if err := store.EnqueueEvent(ctx, pool, user.ID, "force_resync", map[string]any{}, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := store.ClaimEvents(ctx, pool, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 || claimed[0].Kind != "force_resync" {
		t.Fatalf("claimed = %+v, want one force_resync", claimed)
	}

	if err := store.ApplyEvent(ctx, pool, claimed[0]); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := epochOf(t, ctx, pool, principal); got <= before {
		t.Fatalf("epoch = %d after force_resync, want > %d", got, before)
	}
	var dispatchedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT dispatched_at FROM user_events WHERE id = $1`, claimed[0].ID).Scan(&dispatchedAt); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if dispatchedAt == nil {
		t.Fatal("event not marked dispatched")
	}
}

func TestRecordFailureDeadLetters(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()

	user, _, err := store.NewUsers(pool).Signup(ctx, "evuser", "ev@example.com", "Ev", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	if err := store.EnqueueEvent(ctx, pool, user.ID, "force_resync", map[string]any{}, nil); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM user_events`).Scan(&id); err != nil {
		t.Fatalf("read event: %v", err)
	}

	// The first failure schedules a backoff, so the event is not claimable yet.
	if err := store.RecordFailure(ctx, pool, id); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	claimed, err := store.ClaimEvents(ctx, pool, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("backing-off event claimed: %+v", claimed)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT attempts FROM user_events WHERE id = $1`, id).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}

	// Repeated failures (time advanced between them) eventually dead-letter it.
	dead := false
	for i := 0; i < 20 && !dead; i++ {
		if _, err := pool.Exec(ctx, `UPDATE user_events SET next_attempt_at = now() WHERE id = $1`, id); err != nil {
			t.Fatalf("advance time: %v", err)
		}
		if err := store.RecordFailure(ctx, pool, id); err != nil {
			t.Fatalf("record failure: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT dead_letter FROM user_events WHERE id = $1`, id).Scan(&dead); err != nil {
			t.Fatalf("read dead_letter: %v", err)
		}
	}
	if !dead {
		t.Fatal("event never dead-lettered")
	}
	claimed, err = store.ClaimEvents(ctx, pool, 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, c := range claimed {
		if c.ID == id {
			t.Fatal("dead-lettered event was claimed")
		}
	}
}
