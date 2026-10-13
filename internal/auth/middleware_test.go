package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/auth"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/testdb"
)

// TestTokenAsBasicPassword pins the opt-in admission path: a token sent as the
// Basic password resolves to its principal only when the option is on.
func TestTokenAsBasicPassword(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	u, _, err := users.Signup(ctx, "authuser", "auth@example.com", "Auth", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	pr, token, err := store.NewPrincipals(pool).IssueToken(ctx, u.ID, "primary", "phone")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	var got auth.Actor
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = auth.ActorFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	strict := httptest.NewServer(auth.RequireDAV(users, store.NewPrincipals(pool), next))
	defer strict.Close()
	if code := basicProbe(t, strict, "whatever", token); code != http.StatusUnauthorized {
		t.Fatalf("strict token-as-password = %d, want 401", code)
	}

	open := httptest.NewServer(auth.RequireDAV(users, store.NewPrincipals(pool), next, auth.WithTokenPassword(true)))
	defer open.Close()
	if code := basicProbe(t, open, "whatever", token); code != http.StatusOK {
		t.Fatalf("enabled token-as-password = %d, want 200", code)
	}
	if got.PrincipalID == nil || *got.PrincipalID != pr.ID {
		t.Fatalf("resolved actor = %+v, want principal %d", got, pr.ID)
	}

	// A wrong token still fails when the option is on.
	if code := basicProbe(t, open, "whatever", "not-a-token"); code != http.StatusUnauthorized {
		t.Fatalf("bad token-as-password = %d, want 401", code)
	}

	// The account password keeps working, and resolves without a principal.
	got = auth.Actor{}
	if code := basicProbe(t, open, "authuser", "pw"); code != http.StatusOK {
		t.Fatalf("account password = %d, want 200", code)
	}
	if got.PrincipalID != nil {
		t.Fatalf("account actor = %+v, want no principal", got)
	}
}

func basicProbe(t *testing.T, ts *httptest.Server, user, pass string) int {
	t.Helper()
	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func resetPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url, err := testdb.URL("auth")
	if err != nil {
		t.Fatalf("test db: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		pool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if err := store.Migrate(context.Background(), pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	return pool
}
