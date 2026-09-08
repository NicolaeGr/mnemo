package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/model"
	"example.com/segments/internal/store"
)

func TestBookOps(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()

	user, system, err := store.NewUsers(pool).Signup(ctx, "bookuser", "book@example.com", "Book", "pw", 12)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	prim := insertPrincipal(t, ctx, pool, user.ID, "primary")

	// Create is invisible to existing principals (no epoch bump).
	e := epochOf(t, ctx, pool, prim)
	work, err := createBook(t, ctx, pool, user.ID, "work", "Work")
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	if got := epochOf(t, ctx, pool, prim); got != e {
		t.Fatalf("create bumped epoch: %d != %d", got, e)
	}

	// Duplicate slug and system-book deletion are both conflicts.
	if _, err := createBook(t, ctx, pool, user.ID, "work", "Dup"); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("dup slug err = %v, want ErrConflict", err)
	}
	if err := deleteBook(t, ctx, pool, user.ID, system.ID); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("system delete err = %v, want ErrConflict", err)
	}

	// Deactivating and changing tiers flip what principals sync, so each bumps.
	if err := setBookActive(t, ctx, pool, user.ID, work.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got := epochOf(t, ctx, pool, prim); got <= e {
		t.Fatalf("deactivate did not bump: %d <= %d", got, e)
	}
	e = epochOf(t, ctx, pool, prim)
	if err := setBookActive(t, ctx, pool, user.ID, work.ID, true); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if got := epochOf(t, ctx, pool, prim); got <= e {
		t.Fatalf("reactivate did not bump: %d <= %d", got, e)
	}
	e = epochOf(t, ctx, pool, prim)
	if err := setBookTiers(t, ctx, pool, user.ID, work.ID, []string{"archived"}); err != nil {
		t.Fatalf("set tiers: %v", err)
	}
	if got := epochOf(t, ctx, pool, prim); got <= e {
		t.Fatalf("tier change did not bump: %d <= %d", got, e)
	}
	// Same tiers again is a no-op.
	e = epochOf(t, ctx, pool, prim)
	if err := setBookTiers(t, ctx, pool, user.ID, work.ID, []string{"archived"}); err != nil {
		t.Fatalf("set tiers no-op: %v", err)
	}
	if got := epochOf(t, ctx, pool, prim); got != e {
		t.Fatalf("no-op tier set bumped: %d != %d", got, e)
	}

	// Deleting a book folds its tags onto the system book (I1) then bumps.
	contact := insertContact(t, ctx, pool, user.ID, system.ID)
	if _, err := pool.Exec(ctx,
		`INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, contact, work.ID); err != nil {
		t.Fatalf("tag work: %v", err)
	}
	e = epochOf(t, ctx, pool, prim)
	if err := deleteBook(t, ctx, pool, user.ID, work.ID); err != nil {
		t.Fatalf("delete book: %v", err)
	}
	if got := epochOf(t, ctx, pool, prim); got <= e {
		t.Fatalf("delete did not bump: %d <= %d", got, e)
	}
	assertTags(t, ctx, pool, contact, system.ID)
	var gone bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM books WHERE id = $1)`, work.ID).Scan(&gone); err != nil {
		t.Fatalf("book gone: %v", err)
	}
	if gone {
		t.Fatal("deleted book still exists")
	}

	// Mutating an unknown book is not-found.
	if err := setBookActive(t, ctx, pool, user.ID, work.ID, true); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("unknown book err = %v, want ErrNotFound", err)
	}
}

func createBook(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, slug, name string) (store.Book, error) {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "bookuser"}
	var book store.Book
	err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		var err error
		book, err = s.CreateBook(ctx, slug, name, nil, 100)
		return err
	})
	return book, err
}

func setBookActive(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, bookID int64, active bool) error {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "bookuser"}
	return store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.SetBookActive(ctx, bookID, active)
	})
}

func setBookTiers(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, bookID int64, tiers []string) error {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "bookuser"}
	return store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.SetBookTiers(ctx, bookID, tiers)
	})
}

func deleteBook(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, bookID int64) error {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "bookuser"}
	return store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.DeleteBook(ctx, bookID)
	})
}
