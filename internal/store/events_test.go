package store_test

import (
	"context"
	"testing"
	"time"

	"example.com/segments/internal/store"
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
