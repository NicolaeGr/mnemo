package webdavsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/webdavsvc"
)

func TestBearerPrincipalAuth(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, testUsername, testEmail, "Nick", testPassword, 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	u, err := users.ByLogin(ctx, testUsername)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	pr, raw, err := store.NewPrincipals(pool).IssueToken(ctx, u.ID, "primary", "test device")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	ts := httptest.NewServer(webdavsvc.New(users, pool))
	defer ts.Close()
	principal := "/carddav/" + testUsername + "/"

	// A valid bearer principal resolves and is served.
	if code := davProbe(t, ts, principal, "Bearer "+raw); code != http.StatusMultiStatus {
		t.Fatalf("bearer probe = %d, want 207", code)
	}
	// The probe must have hit the token path, not the Basic fallback: Touch
	// only runs for a bearer, so last_synced_at must now be set.
	var touched bool
	if err := pool.QueryRow(ctx,
		`SELECT last_synced_at IS NOT NULL FROM principals WHERE id = $1`, pr.ID).Scan(&touched); err != nil {
		t.Fatalf("read principal: %v", err)
	}
	if !touched {
		t.Fatal("bearer request did not resolve to a principal")
	}
	// Garbage token is rejected.
	if code := davProbe(t, ts, principal, "Bearer nope"); code != http.StatusUnauthorized {
		t.Fatalf("bad token = %d, want 401", code)
	}
	// Missing credentials are rejected.
	if code := davProbe(t, ts, principal, ""); code != http.StatusUnauthorized {
		t.Fatalf("no auth = %d, want 401", code)
	}
}

func davProbe(t *testing.T, ts *httptest.Server, path, authz string) int {
	t.Helper()
	req, err := http.NewRequest("PROPFIND", ts.URL+path, nil)
	if err != nil {
		t.Fatalf("probe req: %v", err)
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
