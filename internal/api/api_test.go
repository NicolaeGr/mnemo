package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

	// Rename a book and reorder it.
	if resp := call(t, ts, "PATCH", base+"/books/"+strconv.FormatInt(bookOut.ID, 10), "", `{"display_name":"Work Renamed"}`); resp.Code != http.StatusNoContent {
		t.Fatalf("rename book = %d, want 204", resp.Code)
	}
	if resp := call(t, ts, "POST", base+"/books/reorder", "", `[`+strconv.FormatInt(bookOut.ID, 10)+`]`); resp.Code != http.StatusNoContent {
		t.Fatalf("reorder = %d, want 204", resp.Code)
	}

	// Fetch and delete a contact by id.
	get := call(t, ts, "GET", base+"/contacts/"+strconv.FormatInt(cid, 10), "", "")
	if get.Code != http.StatusOK {
		t.Fatalf("get contact = %d, want 200", get.Code)
	}
	var full struct {
		ID        int64  `json:"id"`
		VCardText string `json:"vcard_text"`
	}
	decode(t, get, &full)
	if full.ID != cid || !strings.Contains(full.VCardText, "BEGIN:VCARD") {
		t.Fatalf("get contact = %+v", full)
	}
	if resp := call(t, ts, "DELETE", base+"/contacts/"+strconv.FormatInt(cid, 10), "", ""); resp.Code != http.StatusNoContent {
		t.Fatalf("delete contact = %d, want 204", resp.Code)
	}

	if resp := call(t, ts, "GET", base+"/books", "Bearer "+tokenOut.Token, ""); resp.Code != http.StatusOK {
		t.Fatalf("bearer books = %d, want 200", resp.Code)
	}

	// A device can be renamed, and revoking it kills its token.
	pid := strconv.FormatInt(tokenOut.ID, 10)
	if resp := call(t, ts, "PATCH", base+"/principals/"+pid, "", `{"label":"phone-2"}`); resp.Code != http.StatusNoContent {
		t.Fatalf("rename principal = %d, want 204", resp.Code)
	}
	if resp := call(t, ts, "DELETE", base+"/principals/"+pid, "", ""); resp.Code != http.StatusNoContent {
		t.Fatalf("revoke principal = %d, want 204", resp.Code)
	}
	if resp := call(t, ts, "GET", base+"/books", "Bearer "+tokenOut.Token, ""); resp.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d, want 401", resp.Code)
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

func TestContactCreateUpdate(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "apiuser", "api@example.com", "API", "pw", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	ts := httptest.NewServer(api.New(users, pool))
	defer ts.Close()
	base := ts.URL

	card := func(fn string) string {
		return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:new-1\r\nFN:" + fn + "\r\nTEL:+1 555 0300\r\nEND:VCARD\r\n"
	}
	in, _ := json.Marshal(map[string]any{"vcard": card("Carol")})
	created := call(t, ts, "POST", base+"/contacts", "", string(in))
	if created.Code != http.StatusCreated {
		t.Fatalf("create contact = %d, want 201: %s", created.Code, created.Body.String())
	}
	var out struct {
		ID   int64  `json:"id"`
		UID  string `json:"uid"`
		ETag string `json:"etag"`
	}
	decode(t, created, &out)
	if out.ID == 0 || out.ETag == "" {
		t.Fatalf("created contact = %+v", out)
	}

	upd, _ := json.Marshal(map[string]any{"vcard": card("Carol Two")})
	if resp := call(t, ts, "PATCH", base+"/contacts/"+strconv.FormatInt(out.ID, 10), "", string(upd)); resp.Code != http.StatusOK {
		t.Fatalf("update contact = %d, want 200: %s", resp.Code, resp.Body.String())
	}
	get := call(t, ts, "GET", base+"/contacts/"+strconv.FormatInt(out.ID, 10), "", "")
	if get.Code != http.StatusOK {
		t.Fatalf("get after update = %d, want 200", get.Code)
	}
	var full struct {
		VCardText string `json:"vcard_text"`
	}
	decode(t, get, &full)
	if !strings.Contains(full.VCardText, "FN:Carol Two") {
		t.Fatalf("updated vcard = %q, want FN:Carol Two", full.VCardText)
	}
}

func TestContactStructuredInput(t *testing.T) {
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

	if resp := call(t, ts, "POST", base+"/books", "", `{"slug":"work","display_name":"Work"}`); resp.Code != http.StatusCreated {
		t.Fatalf("create book = %d: %s", resp.Code, resp.Body.String())
	}
	var bookID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM books WHERE owner_user_id = $1 AND slug = 'work'`, user.ID).Scan(&bookID); err != nil {
		t.Fatalf("read book: %v", err)
	}

	// The structured shape mirrors the admin form: name parts, property rows,
	// exploded addresses, and tags. Tags are the exact set, so tagging into work
	// moves the card out of the system book.
	in, _ := json.Marshal(map[string]any{
		"name":      map[string]any{"given": "Alice", "family": "Smith"},
		"fields":    []map[string]any{{"kind": "email", "type": "work", "value": "alice@example.com"}},
		"addresses": []map[string]any{{"type": "work", "street": "1 Main St", "city": "Springfield"}},
		"tags":      []int64{bookID},
	})
	created := call(t, ts, "POST", base+"/contacts", "", string(in))
	if created.Code != http.StatusCreated {
		t.Fatalf("create structured = %d: %s", created.Code, created.Body.String())
	}
	var out struct {
		ID        int64  `json:"id"`
		VCardText string `json:"vcard_text"`
		Name      struct {
			Given  string `json:"given"`
			Family string `json:"family"`
		} `json:"name"`
		Addresses []struct {
			Street string `json:"street"`
		} `json:"addresses"`
	}
	decode(t, created, &out)
	for _, want := range []string{"FN:Alice Smith", "N:Smith;Alice", "alice@example.com", "1 Main St"} {
		if !strings.Contains(out.VCardText, want) {
			t.Fatalf("structured vcard missing %q:\n%s", want, out.VCardText)
		}
	}
	if out.Name.Given != "Alice" || out.Name.Family != "Smith" || len(out.Addresses) != 1 || out.Addresses[0].Street != "1 Main St" {
		t.Fatalf("structured echo wrong: %+v", out)
	}

	var inWork, inSystem bool
	if err := pool.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM contact_books WHERE contact_id = $1 AND book_id = $2),
		  EXISTS(SELECT 1 FROM contact_books WHERE contact_id = $1 AND book_id = $3)`,
		out.ID, bookID, system.ID).Scan(&inWork, &inSystem); err != nil {
		t.Fatalf("read tags: %v", err)
	}
	if !inWork || inSystem {
		t.Fatalf("tags = work:%v system:%v, want work only", inWork, inSystem)
	}

	// A structured PATCH replaces the editable fields over the stored identity,
	// and leaves tags untouched when the body omits them.
	patch, _ := json.Marshal(map[string]any{
		"name":   map[string]any{"given": "Alice", "family": "Jones"},
		"fields": []map[string]any{{"kind": "email", "type": "work", "value": "alice@example.com"}},
	})
	if resp := call(t, ts, "PATCH", base+"/contacts/"+strconv.FormatInt(out.ID, 10), "", string(patch)); resp.Code != http.StatusOK {
		t.Fatalf("structured patch = %d: %s", resp.Code, resp.Body.String())
	}
	var after struct {
		VCardText string `json:"vcard_text"`
	}
	decode(t, call(t, ts, "GET", base+"/contacts/"+strconv.FormatInt(out.ID, 10), "", ""), &after)
	if !strings.Contains(after.VCardText, "FN:Alice Jones") || !strings.Contains(after.VCardText, "alice@example.com") {
		t.Fatalf("patch produced wrong card:\n%s", after.VCardText)
	}
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM contact_books WHERE contact_id = $1 AND book_id = $2)`, out.ID, bookID).Scan(&inWork); err != nil || !inWork {
		t.Fatalf("patch dropped tags (inWork=%v err=%v)", inWork, err)
	}
}

func TestAccountEndpoints(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "apiuser", "api@example.com", "API", "pw", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	ts := httptest.NewServer(api.New(users, pool))
	defer ts.Close()
	base := ts.URL

	me := call(t, ts, "GET", base+"/me", "", "")
	if me.Code != http.StatusOK {
		t.Fatalf("GET /me = %d, want 200", me.Code)
	}
	var who struct {
		Username string `json:"username"`
	}
	decode(t, me, &who)
	if who.Username != "apiuser" {
		t.Fatalf("/me username = %q, want apiuser", who.Username)
	}

	// Default book must be an owned book.
	created := call(t, ts, "POST", base+"/books", "", `{"slug":"home","display_name":"Home"}`)
	var book struct {
		ID int64 `json:"id"`
	}
	decode(t, created, &book)
	patch := `{"default_book_id":` + strconv.FormatInt(book.ID, 10) + `}`
	if resp := call(t, ts, "PATCH", base+"/me/settings", "", patch); resp.Code != http.StatusNoContent {
		t.Fatalf("PATCH /me/settings = %d, want 204", resp.Code)
	}

	// Signup is anonymous, then the new user can log in.
	signup := anonRequest(t, ts, "POST", base+"/users", `{"username":"newbie","email":"newbie@example.com","name":"New","password":"secret"}`)
	if signup.Code != http.StatusCreated {
		t.Fatalf("signup = %d, want 201: %s", signup.Code, signup.Body.String())
	}
	if dup := anonRequest(t, ts, "POST", base+"/users", `{"username":"newbie","email":"newbie@example.com","name":"New","password":"secret"}`); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate signup = %d, want 409", dup.Code)
	}
	if bad := anonRequest(t, ts, "POST", base+"/users", `{"username":"Bad_User","email":"bad@example.com","name":"Bad","password":"secret"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad-username signup = %d, want 400", bad.Code)
	}

	req, _ := http.NewRequest("GET", base+"/me", nil)
	req.SetBasicAuth("newbie", "secret")
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("newbie /me = %d, want 200", rec.Code)
	}
}

func TestBookDescriptionAndSearchFilter(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "apiuser", "api@example.com", "API", "pw", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	ts := httptest.NewServer(api.New(users, pool))
	defer ts.Close()
	base := ts.URL

	mkBook := func(slug, name string) int64 {
		t.Helper()
		resp := call(t, ts, "POST", base+"/books", "", `{"slug":"`+slug+`","display_name":"`+name+`"}`)
		if resp.Code != http.StatusCreated {
			t.Fatalf("create book %s = %d: %s", slug, resp.Code, resp.Body.String())
		}
		var out struct {
			ID int64 `json:"id"`
		}
		decode(t, resp, &out)
		return out.ID
	}
	alpha := mkBook("alpha", "Alpha")
	beta := mkBook("beta", "Beta")

	if resp := call(t, ts, "PATCH", base+"/books/"+strconv.FormatInt(alpha, 10), "", `{"description":"alpha desc"}`); resp.Code != http.StatusNoContent {
		t.Fatalf("patch description = %d, want 204: %s", resp.Code, resp.Body.String())
	}
	listed := call(t, ts, "GET", base+"/books", "", "")
	var books []struct {
		ID          int64   `json:"id"`
		Description *string `json:"description"`
	}
	decode(t, listed, &books)
	var gotDesc *string
	for _, b := range books {
		if b.ID == alpha {
			gotDesc = b.Description
		}
	}
	if gotDesc == nil || *gotDesc != "alpha desc" {
		t.Fatalf("alpha description = %v, want alpha desc", gotDesc)
	}

	mkContact := func(fn string, book int64) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
			VALUES ((SELECT id FROM users WHERE username = 'apiuser'), $1, $1, 'BEGIN:VCARD\r\nEND:VCARD', '{}', 'e')
			RETURNING id`, fn+".vcf").Scan(&id); err != nil {
			t.Fatalf("insert contact: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, id, book); err != nil {
			t.Fatalf("tag: %v", err)
		}
		return id
	}
	ca := mkContact("a", alpha)
	cb := mkContact("b", beta)

	search := func(book int64) []int64 {
		t.Helper()
		resp := call(t, ts, "GET", base+"/contacts?book="+strconv.FormatInt(book, 10), "", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("search book=%d = %d: %s", book, resp.Code, resp.Body.String())
		}
		var out struct {
			Contacts []struct {
				ID int64 `json:"id"`
			} `json:"contacts"`
		}
		decode(t, resp, &out)
		ids := make([]int64, 0, len(out.Contacts))
		for _, c := range out.Contacts {
			ids = append(ids, c.ID)
		}
		return ids
	}
	if ids := search(alpha); len(ids) != 1 || ids[0] != ca {
		t.Fatalf("search alpha = %v, want [%d]", ids, ca)
	}
	if ids := search(beta); len(ids) != 1 || ids[0] != cb {
		t.Fatalf("search beta = %v, want [%d]", ids, cb)
	}
}

func anonRequest(t *testing.T, ts *httptest.Server, method, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ts.Config.Handler.ServeHTTP(rec, req)
	return rec
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
