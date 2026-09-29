package store_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/testdb"
)

// TestConcurrentWritesCommitInIDOrder asserts I4: the per-user advisory lock
// serializes writers, so the order they take the lock is the order their
// change ids are allocated. A writer that removes LockSync fails this test.
func TestConcurrentWritesCommitInIDOrder(t *testing.T) {
	ctx := context.Background()
	url, err := testdb.URL("store")
	if err != nil {
		t.Fatalf("test db: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user, _, err := store.NewUsers(pool).Signup(ctx, "concuser", "conc@example.com", "Conc", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	actor := model.Actor{UserID: user.ID, Username: "concuser"}

	const n = 16
	type result struct {
		at time.Time // when the lock was taken (per-statement clock)
		id int64     // the change id allocated under the lock
	}
	ch := make(chan result, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(fn string) {
			var r result
			err := store.WithTx(ctx, pool, actor, func(s store.ScopedStore) error {
				if err := store.LockSync(ctx, s.Tx, user.ID); err != nil {
					return err
				}
				// Hold the lock a beat so lock-take order is observable: clock_timestamp
				// is per-statement, unlike now(), which is fixed at tx start.
				if _, err := s.Tx.Exec(ctx, `SELECT pg_sleep(0.002)`); err != nil {
					return err
				}
				if err := s.Tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&r.at); err != nil {
					return err
				}
				if err := store.AppendChange(ctx, s.Tx, user.ID, fn, "put"); err != nil {
					return err
				}
				return s.Tx.QueryRow(ctx,
					`SELECT id FROM contact_changes WHERE user_id = $1 AND filename = $2`, user.ID, fn).Scan(&r.id)
			})
			if err != nil {
				errs <- err
				return
			}
			ch <- r
		}(fmt.Sprintf("c%03d.vcf", i))
	}

	out := make([]result, 0, n)
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			t.Fatalf("concurrent write: %v", err)
		case r := <-ch:
			out = append(out, r)
		}
	}
	if len(out) != n {
		t.Fatalf("got %d results, want %d", len(out), n)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	for i := 1; i < len(out); i++ {
		if out[i].id <= out[i-1].id {
			t.Fatalf("commit order != id order: %+v", out)
		}
	}

	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM contact_changes WHERE user_id = $1`, user.ID).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != n {
		t.Fatalf("change rows = %d, want %d", total, n)
	}
}
