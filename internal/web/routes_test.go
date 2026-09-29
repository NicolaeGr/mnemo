package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/testdb"
	"github.com/nicolaegr/mnemo/internal/web"
	"github.com/nicolaegr/mnemo/internal/websession"
)

// TestRouterServesUIAndAPI pins the root dispatch: UI paths reach the admin
// handler, /api/v1 reaches REST, and a bare GET / sends the browser to the UI.
func TestRouterServesUIAndAPI(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "ruser", "r@example.com", "R", "pw", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	sess := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := web.New(web.Deps{Store: &store.Store{PG: pool}, Session: sess})

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec
	}

	if rec := get("/login"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sign in") {
		t.Fatalf("GET /login = %d, body has Sign in: %v", rec.Code, strings.Contains(rec.Body.String(), "Sign in"))
	}
	if rec := get("/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/dashboard" {
		t.Fatalf("GET / = %d %q, want 303 /dashboard", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/dashboard"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET /dashboard = %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}

	// The REST API keeps working under the same router.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/users",
		strings.NewReader(`{"username":"anon","email":"anon@example.com","name":"A","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/users = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url, err := testdb.URL("webroute")
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
