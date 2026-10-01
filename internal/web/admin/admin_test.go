package admin_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

	if rec := do(h, "GET", "/dashboard", nil, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Overview") || !strings.Contains(rec.Body.String(), "output.css") {
		t.Fatalf("dashboard = %d, wants Overview and the stylesheet", rec.Code)
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

func TestAdminBooks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	_, system, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, csrf := login(t, h, mgr, "admin", "secret")

	// The slug checker reports availability before the unique constraint fires.
	if rec := do(h, "GET", "/dashboard/books/slug?slug=work", nil, cookie); !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("free slug check = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "GET", "/dashboard/books/slug?slug=all", nil, cookie); !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("system slug should be taken: %s", rec.Body.String())
	}

	rec := do(h, "POST", "/dashboard/books", url.Values{"csrf": {csrf}, "slug": {"work"}, "display_name": {"Work"}, "tiers": {"primary", "secondary"}}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Work") || !strings.Contains(rec.Body.String(), "modal-root") {
		t.Fatalf("create book = %d, body missing Work or modal clear: %s", rec.Code, rec.Body.String())
	}
	var workID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM books WHERE slug = 'work'`).Scan(&workID); err != nil {
		t.Fatalf("read created book: %v", err)
	}
	if rec := do(h, "GET", "/dashboard/books/slug?slug=work", nil, cookie); !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("taken slug check = %d: %s", rec.Code, rec.Body.String())
	}

	// Deactivate the new book via the edit endpoint; the list must show it inactive.
	rec = do(h, "POST", "/dashboard/books/"+strconv.FormatInt(workID, 10)+"/edit",
		url.Values{"csrf": {csrf}, "display_name": {"Work"}, "tiers": {"primary", "secondary"}, "active": {"false"}}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "inactive") {
		t.Fatalf("deactivate = %d, body missing inactive: %s", rec.Code, rec.Body.String())
	}

	// The system book's tiers and active flag are fixed, so editing them is ignored.
	rec = do(h, "POST", "/dashboard/books/"+strconv.FormatInt(system.ID, 10)+"/edit",
		url.Values{"csrf": {csrf}, "display_name": {"Default Contacts"}, "tiers": {"archived"}, "active": {"false"}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("system edit = %d, want 200", rec.Code)
	}
	var sysActive bool
	var sysTiers string
	if err := pool.QueryRow(ctx,
		`SELECT is_active, array_to_string(synced_tiers, ',') FROM books WHERE id = $1`, system.ID).Scan(&sysActive, &sysTiers); err != nil {
		t.Fatalf("read system book: %v", err)
	}
	if !sysActive || !strings.Contains(sysTiers, "primary") {
		t.Fatalf("system book changed: active=%v tiers=%q", sysActive, sysTiers)
	}

	rec = do(h, "POST", "/dashboard/books/"+strconv.FormatInt(workID, 10)+"/delete",
		url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}
	var gone bool
	if err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM books WHERE id = $1)`, workID).Scan(&gone); err != nil || !gone {
		t.Fatalf("book not deleted (gone=%v err=%v)", gone, err)
	}

	// A mutation without the CSRF token is refused.
	if rec := do(h, "POST", "/dashboard/books", url.Values{"slug": {"x"}, "display_name": {"X"}}, cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("create without csrf = %d, want 403", rec.Code)
	}
}

func TestAdminDevices(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, csrf := login(t, h, mgr, "admin", "secret")

	rec := do(h, "POST", "/dashboard/devices", url.Values{"csrf": {csrf}, "label": {"phone"}, "tier": {"primary"}}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "New device token") {
		t.Fatalf("create device = %d, body missing token banner", rec.Code)
	}
	var pid int64
	if err := pool.QueryRow(ctx, `SELECT id FROM principals WHERE label = 'phone'`).Scan(&pid); err != nil {
		t.Fatalf("read device: %v", err)
	}

	rec = do(h, "POST", "/dashboard/books", url.Values{"csrf": {csrf}, "slug": {"work"}, "display_name": {"Work"}, "tiers": {"primary", "secondary"}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("create book = %d", rec.Code)
	}
	var bookID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM books WHERE slug = 'work'`).Scan(&bookID); err != nil {
		t.Fatalf("read book: %v", err)
	}

	// One edit call changes the tier and hides a book.
	rec = do(h, "POST", "/dashboard/devices/"+strconv.FormatInt(pid, 10)+"/edit",
		url.Values{"csrf": {csrf}, "label": {"phone"}, "tier": {"secondary"}, "hide": {strconv.FormatInt(bookID, 10)}}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "secondary") {
		t.Fatalf("edit device = %d, body missing secondary", rec.Code)
	}
	var hidden bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM principal_book_overrides
		               WHERE principal_id = $1 AND book_id = $2 AND enabled = false)`, pid, bookID).Scan(&hidden); err != nil || !hidden {
		t.Fatalf("override not recorded (hidden=%v err=%v)", hidden, err)
	}

	// The edit modal seeds the chip picker with the hidden book instead of checkboxes.
	rec = do(h, "GET", "/dashboard/devices/"+strconv.FormatInt(pid, 10)+"/edit", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "multiSelect(") ||
		!strings.Contains(rec.Body.String(), `&#34;selected&#34;:[`+strconv.FormatInt(bookID, 10)+`]`) {
		t.Fatalf("device modal missing picker seed: %s", rec.Body.String())
	}

	rec = do(h, "POST", "/dashboard/devices/"+strconv.FormatInt(pid, 10)+"/delete",
		url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", rec.Code)
	}
	var gone bool
	if err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM principals WHERE id = $1)`, pid).Scan(&gone); err != nil || !gone {
		t.Fatalf("device not revoked (gone=%v err=%v)", gone, err)
	}
}

func TestAdminContacts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	user, system, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, csrf := login(t, h, mgr, "admin", "secret")

	do(h, "POST", "/dashboard/books", url.Values{"csrf": {csrf}, "slug": {"work"}, "display_name": {"Work"}, "tiers": {"primary", "secondary"}}, cookie)
	var workID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM books WHERE slug = 'work'`).Scan(&workID); err != nil {
		t.Fatalf("read book: %v", err)
	}

	var cid int64
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:alice\r\nFN:Alice Example\r\nEND:VCARD\r\n"
	if err := pool.QueryRow(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, 'alice.vcf', 'urn:uuid:alice', $2, '{"fn":"Alice Example"}', 'e') RETURNING id`, user.ID, card).Scan(&cid); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, cid, system.ID); err != nil {
		t.Fatalf("tag system: %v", err)
	}

	if rec := do(h, "GET", "/dashboard/contacts", nil, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Alice Example") {
		t.Fatalf("contacts page = %d, body missing name", rec.Code)
	}
	if rec := do(h, "GET", "/dashboard/contacts/search?q=Alice", nil, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Alice Example") {
		t.Fatalf("search = %d, body missing name", rec.Code)
	}

	rec := do(h, "POST", "/dashboard/contacts/"+strconv.FormatInt(cid, 10)+"/edit",
		url.Values{"csrf": {csrf}, "n_given": {"Alice"}, "n_family": {"Example"}, "book": {strconv.FormatInt(workID, 10)}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("tag = %d, want 200", rec.Code)
	}
	var tagged bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM contact_books WHERE contact_id = $1 AND book_id = $2)`, cid, workID).Scan(&tagged); err != nil || !tagged {
		t.Fatalf("contact not tagged (tagged=%v err=%v)", tagged, err)
	}
	var inDefault bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM contact_books WHERE contact_id = $1 AND book_id = $2)`, cid, system.ID).Scan(&inDefault); err != nil || inDefault {
		t.Fatalf("contact stayed in the default book (inDefault=%v err=%v)", inDefault, err)
	}

	rec = do(h, "POST", "/dashboard/contacts/"+strconv.FormatInt(cid, 10)+"/delete",
		url.Values{"csrf": {csrf}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}
	var deleted bool
	if err := pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM contacts WHERE id = $1`, cid).Scan(&deleted); err != nil || !deleted {
		t.Fatalf("contact not soft-deleted (deleted=%v err=%v)", deleted, err)
	}
}

func TestAdminSettingsAndContactCreate(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, csrf := login(t, h, mgr, "admin", "secret")

	// Point the default book at a freshly created one.
	rec := do(h, "POST", "/dashboard/books", url.Values{"csrf": {csrf}, "slug": {"work"}, "display_name": {"Work"}, "tiers": {"primary", "secondary"}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("create book = %d", rec.Code)
	}
	var bookID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM books WHERE slug = 'work'`).Scan(&bookID); err != nil {
		t.Fatalf("read book: %v", err)
	}
	rec = do(h, "POST", "/dashboard/settings",
		url.Values{"csrf": {csrf}, "default_book_id": {strconv.FormatInt(bookID, 10)}}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("save settings = %d, want 200", rec.Code)
	}
	var def *int64
	if err := pool.QueryRow(ctx, `
		SELECT default_book_id FROM user_settings s JOIN users u ON u.id = s.user_id
		 WHERE u.username = 'admin'`).Scan(&def); err != nil || def == nil || *def != bookID {
		t.Fatalf("default book = %v, want %d (err=%v)", def, bookID, err)
	}

	// Create a contact from the form: name parts plus a property row.
	rec = do(h, "POST", "/dashboard/contacts",
		url.Values{
			"csrf": {csrf}, "n_given": {"Alice"}, "n_family": {"Example"},
			"row_kind": {"email"}, "row_type": {"work"}, "row_key": {""}, "row_value": {"alice@example.com"},
		}, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Alice Example") {
		t.Fatalf("create contact = %d, body missing name", rec.Code)
	}
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM contacts c JOIN users u ON u.id = c.user_id
		 WHERE u.username = 'admin' AND c.deleted_at IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("contact count = %d (err=%v), want 1", count, err)
	}
}

func TestAdminContactEdit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	user, system, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, csrf := login(t, h, mgr, "admin", "secret")

	var cid int64
	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:edit\r\nFN:Alice\r\nORG:Acme\r\nEND:VCARD\r\n"
	if err := pool.QueryRow(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, 'alice.vcf', 'urn:uuid:edit', $2, '{}', 'e') RETURNING id`, user.ID, card).Scan(&cid); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO contact_books (contact_id, book_id) VALUES ($1, $2)`, cid, system.ID); err != nil {
		t.Fatalf("tag system: %v", err)
	}

	// Structured name plus typed rows, including a custom key that must survive.
	rec := do(h, "POST", "/dashboard/contacts/"+strconv.FormatInt(cid, 10)+"/edit",
		url.Values{
			"csrf":     {csrf},
			"n_prefix": {"Dr."}, "n_given": {"Alice"}, "n_family": {"Smith"},
			"row_kind":    {"tel", "email", "custom"},
			"row_type":    {"cell", "work", ""},
			"row_key":     {"", "", "ORG"},
			"row_value":   {"+15550111", "a@b.c", "Acme"},
			"adr_type":    {"work"},
			"adr_street":  {"1 Main St"},
			"adr_city":    {"Springfield"},
			"adr_pobox":   {""},
			"adr_ext":     {""},
			"adr_region":  {"IL"},
			"adr_postal":  {"62701"},
			"adr_country": {"USA"},
		}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var text string
	var metaJSON []byte
	if err := pool.QueryRow(ctx, `SELECT vcard_text, search_meta FROM contacts WHERE id = $1`, cid).Scan(&text, &metaJSON); err != nil {
		t.Fatalf("read vcard: %v", err)
	}
	for _, want := range []string{"FN:Dr. Alice Smith", "N:Smith;Alice", "ORG:Acme", "a@b.c", "+15550111", "1 Main St", "Springfield"} {
		if !strings.Contains(text, want) {
			t.Fatalf("edited vcard missing %q:\n%s", want, text)
		}
	}
	var meta map[string]any
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatalf("search_meta: %v", err)
	}
	if meta["fn"] != "Dr. Alice Smith" || meta["n"] != "Dr. Alice Smith" {
		t.Fatalf("search_meta fn/n = %v/%v, want Dr. Alice Smith", meta["fn"], meta["n"])
	}

	// Surname is searchable through the derived name.
	if rec := do(h, "GET", "/dashboard/contacts/search?q=Smith", nil, cookie); !strings.Contains(rec.Body.String(), "Dr. Alice Smith") {
		t.Fatalf("surname search missed the contact: %s", rec.Body.String())
	}

	// The edit modal re-derives the exploded address fields from the stored ADR.
	if rec := do(h, "GET", "/dashboard/contacts/"+strconv.FormatInt(cid, 10)+"/edit", nil, cookie); !strings.Contains(rec.Body.String(), "1 Main St") {
		t.Fatalf("edit modal missing address: %s", rec.Body.String())
	}
}

func TestAdminSidebarOOB(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, _ := login(t, h, mgr, "admin", "secret")

	// A fragment navigation returns the leaf plus an OOB refresh of the sidebar
	// so its active state tracks the new page.
	req := httptest.NewRequest("GET", "/dashboard/books", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-Mounted-Segments", "root,dashboard")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "sidebar-nav") || !strings.Contains(body, "hx-swap-oob") {
		t.Fatalf("fragment = %d, missing OOB sidebar: %s", rec.Code, body)
	}
}

func login(t *testing.T, h http.Handler, mgr *websession.Manager, user, pass string) (*http.Cookie, string) {
	t.Helper()
	rec := do(h, "POST", "/login", url.Values{"username": {user}, "password": {pass}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200", rec.Code)
	}
	cookie := sessionCookie(t, rec)
	return cookie, csrfFor(t, mgr, cookie)
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
