package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/testdb"
	"github.com/nicolaegr/mnemo/internal/web/admin"
	"github.com/nicolaegr/mnemo/internal/websession"
)

func TestAdminLoginAndSession(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)

	if rec := do(h, "GET", "/login", nil, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sign in") {
		t.Fatalf("GET /login = %d, body has Sign in: %v", rec.Code, strings.Contains(rec.Body.String(), "Sign in"))
	}

	if rec := do(h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"wrong"}}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rec.Code)
	}

	if rec := do(h, "GET", "/dashboard", nil, nil); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("unauthed dashboard = %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}

	rec := do(h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"secret"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec)

	csrf := csrfFor(t, mgr, cookie)

	if rec := do(h, "GET", "/dashboard", nil, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Overview") {
		t.Fatalf("dashboard = %d, wants Overview", rec.Code)
	}

	// An htmx navigation without a session is redirected via the HX-Redirect header.
	hx := httptest.NewRequest("GET", "/dashboard", nil)
	hx.Header.Set("HX-Request", "true")
	hxRec := httptest.NewRecorder()
	h.ServeHTTP(hxRec, hx)
	if hxRec.Code != http.StatusOK || hxRec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("htmx unauth = %d %q, want 200 + HX-Redirect /login", hxRec.Code, hxRec.Header().Get("HX-Redirect"))
	}

	// Logout is a cookie-authenticated mutation, so it needs the CSRF token.
	if rec := do(h, "POST", "/logout", url.Values{}, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf = %d, want 403", rec.Code)
	}
	if rec := do(h, "POST", "/logout", url.Values{"csrf": {csrf}}, cookie); rec.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d, want 303", rec.Code)
	}
}

func do(h http.Handler, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" {
			return c
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

func csrfFor(t *testing.T, mgr *websession.Manager, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.AddCookie(cookie)
	sess, ok := mgr.Read(req)
	if !ok {
		t.Fatal("could not read the issued session")
	}
	return sess.CSRF
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url, err := testdb.URL("admin")
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
