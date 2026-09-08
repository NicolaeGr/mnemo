package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/api"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/testdb"
)

func TestAPI(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	user, system, err := users.Signup(ctx, "apiuser", "api@example.com", "API", "pw", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}

	ts := httptest.NewServer(api.New(users, pool))
	defer ts.Close()
	base := ts.URL

	resp := call(t, ts, "POST", base+"/principals", "", `{"tier":"secondary","label":"phone"}`)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create principal = %d, want 201: %s", resp.Code, resp.Body.String())
	}
	var tokenOut struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	decode(t, resp, &tokenOut)
	if tokenOut.ID == 0 || tokenOut.Token == "" {
		t.Fatal("principal response missing id or token")
	}

	resp = call(t, ts, "POST", base+"/books", "", `{"slug":"work","display_name":"Work"}`)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create book = %d, want 201: %s", resp.Code, resp.Body.String())
	}
	var bookOut struct {
		ID int64 `json:"id"`
	}
	decode(t, resp, &bookOut)

	var cid int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, 'c.vcf', 'c', 'BEGIN:VCARD\r\nEND:VCARD', '{}', 'e') RETURNING id`,
		user.ID).Scan(&cid); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, cid, system.ID); err != nil {
		t.Fatalf("tag system: %v", err)
	}

	body := `{"add":[` + strconv.FormatInt(bookOut.ID, 10) + `]}`
	resp = call(t, ts, "POST", base+"/contacts/"+strconv.FormatInt(cid, 10)+"/tags", "", body)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("tag contact = %d, want 204: %s", resp.Code, resp.Body.String())
	}
	var tagged bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM contact_books WHERE contact_id = $1 AND book_id = $2)`,
		cid, bookOut.ID).Scan(&tagged); err != nil {
		t.Fatalf("read tags: %v", err)
	}
	if !tagged {
		t.Fatal("contact was not tagged into the new book")
	}

	if resp := call(t, ts, "GET", base+"/books", "Bearer "+tokenOut.Token, ""); resp.Code != http.StatusOK {
		t.Fatalf("bearer books = %d, want 200", resp.Code)
	}

	// Wrong credentials are rejected.
	req, err := http.NewRequest("GET", base+"/books", nil)
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.SetBasicAuth("apiuser", "wrong")
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password = %d, want 401", rec.Code)
	}
}

func call(t *testing.T, ts *httptest.Server, method, url, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authz != "" {
		req.Header.Set("Authorization", authz)
	} else {
		req.SetBasicAuth("apiuser", "pw")
	}
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

func resetPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url, err := testdb.URL("api")
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
