package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/testdb"
)

func TestRetag(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()

	user, system, err := store.NewUsers(pool).Signup(ctx, "retaguser", "retag@example.com", "Retag", "pw", 12)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	private := insertBook(t, ctx, pool, user.ID, "private", "{archived}")
	home := insertBook(t, ctx, pool, user.ID, "home", "{primary}")
	archived := insertPrincipal(t, ctx, pool, user.ID, "archived")
	primary := insertPrincipal(t, ctx, pool, user.ID, "primary")

	contact := insertContact(t, ctx, pool, user.ID, system.ID)

	// Moving the card into and out of the archived-only book flips the archived
	// principal's view each time (epoch bumps), but not the primary principal's.
	epochArch := epochOf(t, ctx, pool, archived)
	epochPrim := epochOf(t, ctx, pool, primary)

	retag(t, ctx, pool, user.ID, contact, []int64{private.ID}, nil)
	if got := epochOf(t, ctx, pool, archived); got <= epochArch {
		t.Fatalf("archived principal epoch = %d after reveal, want > %d", got, epochArch)
	}
	if got := epochOf(t, ctx, pool, primary); got != epochPrim {
		t.Fatalf("primary principal bumped on an archived-only flip: %d != %d", got, epochPrim)
	}
	assertTags(t, ctx, pool, contact, system.ID, private.ID)

	epochArch = epochOf(t, ctx, pool, archived)
	retag(t, ctx, pool, user.ID, contact, nil, []int64{private.ID})
	if got := epochOf(t, ctx, pool, archived); got <= epochArch {
		t.Fatalf("archived principal epoch = %d after conceal, want > %d", got, epochArch)
	}
	assertTags(t, ctx, pool, contact, system.ID)

	// Re-removing an already-absent book and detaching the system book change
	// nothing, so neither principal should be bumped again.
	epochArch = epochOf(t, ctx, pool, archived)
	retag(t, ctx, pool, user.ID, contact, nil, []int64{private.ID})
	retag(t, ctx, pool, user.ID, contact, nil, []int64{system.ID})
	assertTags(t, ctx, pool, contact, system.ID)
	if got := epochOf(t, ctx, pool, archived); got != epochArch {
		t.Fatalf("archived principal epoch changed on a no-op retag: %d != %d", got, epochArch)
	}

	// Adding/removing a book the primary principal already sees is invisible on
	// the wire (same URL, same etag), so no epoch bump.
	epochPrim = epochOf(t, ctx, pool, primary)
	retag(t, ctx, pool, user.ID, contact, []int64{home.ID}, nil)
	retag(t, ctx, pool, user.ID, contact, nil, []int64{home.ID})
	if got := epochOf(t, ctx, pool, primary); got != epochPrim {
		t.Fatalf("primary principal bumped on within-visibility retag: %d != %d", got, epochPrim)
	}
}

func retag(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, contactID int64, add, remove []int64) {
	t.Helper()
	actor := model.Actor{UserID: userID, Username: "retaguser"}
	err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, userID); err != nil {
			return err
		}
		return s.Retag(ctx, contactID, add, remove)
	})
	if err != nil {
		t.Fatalf("retag: %v", err)
	}
}

func assertTags(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contactID int64, want ...int64) {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT book_id FROM contact_books WHERE contact_id = $1 ORDER BY book_id`, contactID)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tags = %v, want %v", got, want)
		}
	}
}

func insertBook(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner int64, slug, tiers string) store.Book {
	t.Helper()
	var b store.Book
	err := pool.QueryRow(ctx, `
		INSERT INTO books (owner_user_id, slug, display_name, synced_tiers)
		VALUES ($1, $2, $3, $4::principal_tier[])
		RETURNING id, owner_user_id, slug, display_name, description, sort_order,
		          is_active, is_system, synced_tiers::text[], created_at`,
		owner, slug, slug, tiers,
	).Scan(&b.ID, &b.OwnerUserID, &b.Slug, &b.DisplayName, &b.Description,
		&b.SortOrder, &b.IsActive, &b.IsSystem, &b.SyncedTiers, &b.CreatedAt)
	if err != nil {
		t.Fatalf("insert book: %v", err)
	}
	return b
}

func insertPrincipal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, tier string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO principals (user_id, tier, label) VALUES ($1, $2, $3) RETURNING id`,
		userID, tier, tier+" device").Scan(&id); err != nil {
		t.Fatalf("insert principal: %v", err)
	}
	return id
}

func insertContact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID, systemBook int64) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, 'retag.vcf', 'retag', 'BEGIN:VCARD\r\nEND:VCARD', '{}', 'etag')
		RETURNING id`, userID).Scan(&id)
	if err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, id, systemBook); err != nil {
		t.Fatalf("tag contact: %v", err)
	}
	return id
}

func epochOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, principalID int64) int64 {
	t.Helper()
	var e int64
	if err := pool.QueryRow(ctx,
		`SELECT sync_epoch FROM principals WHERE id = $1`, principalID).Scan(&e); err != nil {
		t.Fatalf("epoch: %v", err)
	}
	return e
}

func resetPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url, err := testdb.URL("store")
	if err != nil {
		t.Fatalf("test db: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := store.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
