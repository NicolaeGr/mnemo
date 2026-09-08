package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/resolve"
	"github.com/nicolaegr/mnemo/internal/store"
)

func TestPrincipalOverrideAndTier(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()

	user, system, err := store.NewUsers(pool).Signup(ctx, "ovuser", "ov@example.com", "Ov", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	work := insertBook(t, ctx, pool, user.ID, "work", "{secondary}")
	principal := insertPrincipal(t, ctx, pool, user.ID, "secondary")

	contact := insertContact(t, ctx, pool, user.ID, system.ID)
	if _, err := pool.Exec(ctx,
		`INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, contact, work.ID); err != nil {
		t.Fatalf("tag work: %v", err)
	}

	principalActor := model.Actor{UserID: user.ID, Username: "ovuser", PrincipalID: &principal}
	visible := func() map[int64]bool {
		t.Helper()
		var out map[int64]bool
		err := store.WithTx(ctx, pool, principalActor, func(s store.ScopedStore) error {
			out, err = resolve.Resolver{}.VisibleBooks(ctx, s)
			return err
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return out
	}

	if !visible()[work.ID] {
		t.Fatal("secondary principal should see the work book before the override")
	}

	// Opting the principal out of the book hides it and bumps its epoch.
	e := epochOf(t, ctx, pool, principal)
	if err := setOverride(t, ctx, pool, user.ID, principal, work.ID, false); err != nil {
		t.Fatalf("set override: %v", err)
	}
	if got := epochOf(t, ctx, pool, principal); got <= e {
		t.Fatalf("override did not bump epoch: %d <= %d", got, e)
	}
	if visible()[work.ID] {
		t.Fatal("work book still visible after override disabled it")
	}

	// Clearing it restores visibility and bumps again.
	e = epochOf(t, ctx, pool, principal)
	if err := clearOverride(t, ctx, pool, user.ID, principal, work.ID); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	if got := epochOf(t, ctx, pool, principal); got <= e {
		t.Fatalf("clear did not bump epoch: %d <= %d", got, e)
	}
	if !visible()[work.ID] {
		t.Fatal("work book not visible after override cleared")
	}

	// Demoting to archived hides everything (system covers primary+secondary).
	e = epochOf(t, ctx, pool, principal)
	if err := setTier(t, ctx, pool, user.ID, principal, "archived"); err != nil {
		t.Fatalf("set tier: %v", err)
	}
	if got := epochOf(t, ctx, pool, principal); got <= e {
		t.Fatalf("tier change did not bump epoch: %d <= %d", got, e)
	}
	if seen := anyTrue(visible()); seen {
		t.Fatalf("archived principal sees a book, want none")
	}
}

func anyTrue(m map[int64]bool) bool {
	for _, ok := range m {
		if ok {
			return true
		}
	}
	return false
}

func setOverride(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, principalID, bookID int64, enabled bool) error {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "ovuser"}
	return store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.SetPrincipalOverride(ctx, principalID, bookID, enabled)
	})
}

func clearOverride(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, principalID, bookID int64) error {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "ovuser"}
	return store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.ClearPrincipalOverride(ctx, principalID, bookID)
	})
}

func setTier(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, principalID int64, tier string) error {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "ovuser"}
	return store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.SetPrincipalTier(ctx, principalID, tier)
	})
}
