package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

// TestCategoriesFollowBooks pins the rule that a card's CATEGORIES lists its
// non-system book names and is rewritten whenever the tags or a book name change.
func TestCategoriesFollowBooks(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	user, system, err := store.NewUsers(pool).Signup(ctx, "catuser", "cat@example.com", "Cat", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	work := insertBook(t, ctx, pool, user.ID, "work", "{primary}")
	contact := insertContact(t, ctx, pool, user.ID, system.ID)

	withLockedTx(t, ctx, pool, user.ID, func(s store.ScopedStore) error {
		return s.Retag(ctx, contact, []int64{work.ID}, nil)
	})
	if got := storedVCard(t, ctx, pool, contact); !strings.Contains(got, "CATEGORIES:work\n") {
		t.Fatalf("after tag, body = %q, want CATEGORIES:work", got)
	}

	withLockedTx(t, ctx, pool, user.ID, func(s store.ScopedStore) error {
		return s.RenameBook(ctx, work.ID, "Day Job")
	})
	if got := storedVCard(t, ctx, pool, contact); !strings.Contains(got, "CATEGORIES:Day Job\n") {
		t.Fatalf("after rename, body = %q, want CATEGORIES:Day Job", got)
	}

	withLockedTx(t, ctx, pool, user.ID, func(s store.ScopedStore) error {
		return s.Retag(ctx, contact, nil, []int64{work.ID})
	})
	if got := storedVCard(t, ctx, pool, contact); strings.Contains(got, "CATEGORIES") {
		t.Fatalf("after untag, body = %q, want no CATEGORIES", got)
	}
}

func withLockedTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, fn func(store.ScopedStore) error) {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "catuser"}
	err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return fn(s)
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
}

func storedVCard(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contactID int64) string {
	t.Helper()
	var text string
	if err := pool.QueryRow(ctx, `SELECT vcard_text FROM contacts WHERE id = $1`, contactID).Scan(&text); err != nil {
		t.Fatalf("read vcard: %v", err)
	}
	return text
}
