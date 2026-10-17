package admin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/admin"
	"github.com/nicolaegr/mnemo/internal/websession"
)

func TestAdminImportAndExport(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	user, err := users.Signup(ctx, "admin", "admin@example.com", "Admin", "secret", 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	mgr := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := admin.New(users, pool, mgr)
	cookie, csrf := login(t, h, mgr, "admin", "secret")

	vcf := strings.Join([]string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"UID:urn:uuid:alice",
		"FN:Alice Example",
		"CATEGORIES:Work,Family",
		"END:VCARD",
		"BEGIN:VCARD",
		"VERSION:3.0",
		"UID:urn:uuid:bob",
		"FN:Bob Example",
		"CATEGORIES:Work",
		"END:VCARD",
		"BEGIN:VCARD",
		"VERSION:3.0",
		"UID:urn:uuid:carol",
		"FN:Carol Example",
		"END:VCARD",
	}, "\r\n")

	rec := doMultipart(t, h, "/dashboard/contacts/import", csrf, "contacts.vcf", []byte(vcf), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/contacts?imported=3&lists=2&skipped=0" {
		t.Fatalf("import redirect = %q", loc)
	}

	var live int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM contacts WHERE user_id = $1 AND deleted_at IS NULL`, user.ID).Scan(&live); err != nil {
		t.Fatalf("count contacts: %v", err)
	}
	if live != 3 {
		t.Fatalf("live contacts = %d, want 3", live)
	}

	var workID int64
	rows, err := pool.Query(ctx, `
		SELECT id, slug FROM books WHERE owner_user_id = $1 ORDER BY slug`, user.ID)
	if err != nil {
		t.Fatalf("read books: %v", err)
	}
	slugs := make([]string, 0)
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			t.Fatalf("scan book: %v", err)
		}
		slugs = append(slugs, slug)
		if slug == "work" {
			workID = id
		}
	}
	rows.Close()
	if strings.Join(slugs, ",") != "family,work" {
		t.Fatalf("books = %v, want family,work", slugs)
	}
	for _, slug := range slugs {
		var approved bool
		if err := pool.QueryRow(ctx,
			`SELECT is_approved FROM books WHERE owner_user_id = $1 AND slug = $2`,
			user.ID, slug).Scan(&approved); err != nil || !approved {
			t.Fatalf("book %s approved = %v (err=%v)", slug, approved, err)
		}
	}

	var aliceText string
	if err := pool.QueryRow(ctx,
		`SELECT vcard_text FROM contacts WHERE user_id = $1 AND uid = 'urn:uuid:alice'`,
		user.ID).Scan(&aliceText); err != nil {
		t.Fatalf("read alice: %v", err)
	}
	for _, want := range []string{"CATEGORIES:Family", "CATEGORIES:Work"} {
		if !strings.Contains(aliceText, want) {
			t.Fatalf("alice categories missing %q:\n%s", want, aliceText)
		}
	}

	var tagged int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM contact_books cb
		  JOIN contacts c ON c.id = cb.contact_id
		 WHERE c.user_id = $1 AND c.uid = 'urn:uuid:alice'`, user.ID).Scan(&tagged); err != nil || tagged != 2 {
		t.Fatalf("alice tags = %d (err=%v), want 2", tagged, err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM contact_books cb
		  JOIN contacts c ON c.id = cb.contact_id
		 WHERE c.user_id = $1 AND c.uid = 'urn:uuid:carol'`, user.ID).Scan(&tagged); err != nil || tagged != 0 {
		t.Fatalf("carol tags = %d (err=%v), want 0", tagged, err)
	}

	var aliceID, bobID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM contacts WHERE user_id = $1 AND uid = 'urn:uuid:alice'`, user.ID).Scan(&aliceID); err != nil {
		t.Fatalf("read alice id: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM contacts WHERE user_id = $1 AND uid = 'urn:uuid:bob'`, user.ID).Scan(&bobID); err != nil {
		t.Fatalf("read bob id: %v", err)
	}

	// The contacts page turns the redirect counters into a notice.
	if rec := do(h, "GET", "/dashboard/contacts?imported=3&lists=2&skipped=0", nil, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Imported 3 contacts.") {
		t.Fatalf("contacts notice = %d: %s", rec.Code, rec.Body.String())
	}

	// Both transfer modals render, and the list export modal carries its scope.
	if rec := do(h, "GET", "/dashboard/contacts/import/modal", nil, cookie); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "multipart/form-data") {
		t.Fatalf("import modal = %d: %s", rec.Code, rec.Body.String())
	}

	// One contact: its own name and no zip offer.
	rec = do(h, "GET", "/dashboard/contacts/export/modal?id="+strconv.FormatInt(aliceID, 10), nil, cookie)
	body := rec.Body.String()
	if !strings.Contains(body, `value="Alice_Example"`) || strings.Contains(body, "Format") || strings.Contains(body, "Zip archive") {
		t.Fatalf("single export modal = %d: %s", rec.Code, body)
	}
	if strings.Contains(body, "Exclude other lists") {
		t.Fatalf("single export modal outside a list offers the list-only category: %s", body)
	}

	// Two contacts: enumerate both in the name and offer zip.
	rec = do(h, "GET", "/dashboard/contacts/export/modal?id="+strconv.FormatInt(aliceID, 10)+"&id="+strconv.FormatInt(bobID, 10), nil, cookie)
	body = rec.Body.String()
	if !strings.Contains(body, `value="Alice_Example and Bob_Example"`) || !strings.Contains(body, "Zip archive") {
		t.Fatalf("multi export modal = %d: %s", rec.Code, body)
	}

	// A list export enumerates its three-or-fewer contacts and shows three
	// category cards in one row.
	rec = do(h, "GET", "/dashboard/contacts/export/modal?book="+strconv.FormatInt(workID, 10), nil, cookie)
	body = rec.Body.String()
	if !strings.Contains(body, `value="Alice_Example and Bob_Example"`) ||
		!strings.Contains(body, "Exclude other lists") || !strings.Contains(body, "grid-cols-3") {
		t.Fatalf("list export modal = %d: %s", rec.Code, body)
	}

	// A whole-book export keeps CATEGORIES.
	rec = do(h, "GET", "/dashboard/contacts/export?format=vcf&categories=include", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("export vcf = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `contacts.vcf`) {
		t.Fatalf("content-disposition = %q", got)
	}
	body = rec.Body.String()
	if strings.Count(body, "BEGIN:VCARD") != 3 {
		t.Fatalf("export has %d cards, want 3:\n%s", strings.Count(body, "BEGIN:VCARD"), body)
	}
	if !strings.Contains(body, "CATEGORIES:Work") {
		t.Fatalf("export missing categories:\n%s", body)
	}

	// The submitted name becomes the download filename.
	rec = do(h, "GET", "/dashboard/contacts/export?format=vcf&categories=include&name=Team_Export", nil, cookie)
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `Team_Export.vcf`) {
		t.Fatalf("named content-disposition = %q", got)
	}

	// Excluding categories strips every CATEGORIES property.
	rec = do(h, "GET", "/dashboard/contacts/export?format=vcf&categories=exclude", nil, cookie)
	if strings.Contains(rec.Body.String(), "CATEGORIES:") {
		t.Fatalf("export still has categories:\n%s", rec.Body.String())
	}

	// A list export with the other lists excluded keeps only that list's name.
	rec = do(h, "GET",
		"/dashboard/contacts/export?format=vcf&categories=only&book="+strconv.FormatInt(workID, 10),
		nil, cookie)
	body = rec.Body.String()
	if strings.Count(body, "BEGIN:VCARD") != 2 {
		t.Fatalf("list export has %d cards, want 2:\n%s", strings.Count(body, "BEGIN:VCARD"), body)
	}
	if strings.Contains(body, "CATEGORIES:Family") || !strings.Contains(body, "CATEGORIES:Work") {
		t.Fatalf("list export categories wrong:\n%s", body)
	}

	// The zip format has one .vcf entry per contact.
	rec = do(h, "GET", "/dashboard/contacts/export?format=zip", nil, cookie)
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("zip content-type = %q", ct)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if len(zr.File) != 3 {
		t.Fatalf("zip entries = %d, want 3", len(zr.File))
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".vcf") {
			t.Fatalf("zip entry %q is not a .vcf", f.Name)
		}
	}

	// A zip of vCards imports through the same path.
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, err := zw.Create("dave.vcf")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	_, _ = io.WriteString(w, "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:dave\r\nFN:Dave Example\r\nCATEGORIES:School\r\nEND:VCARD\r\n")
	_ = zw.Close()

	rec = doMultipart(t, h, "/dashboard/contacts/import", csrf, "contacts.zip", zbuf.Bytes(), cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("zip import = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard/contacts?imported=1&lists=1&skipped=0" {
		t.Fatalf("zip import redirect = %q", loc)
	}

	// Importing the same vCards again skips every duplicate without creating lists.
	rec = doMultipart(t, h, "/dashboard/contacts/import", csrf, "contacts.vcf", []byte(vcf), cookie)
	if loc := rec.Header().Get("Location"); loc != "/dashboard/contacts?imported=0&lists=0&skipped=3" {
		t.Fatalf("duplicate import redirect = %q", loc)
	}

	// More than three contacts fall back to the plain name.
	rec = do(h, "GET", "/dashboard/contacts/export/modal", nil, cookie)
	body = rec.Body.String()
	if !strings.Contains(body, `value="contacts"`) || !strings.Contains(body, "Zip archive") {
		t.Fatalf("all-contacts export modal = %d: %s", rec.Code, body)
	}
}

func doMultipart(t *testing.T, h http.Handler, target, csrf, filename string, data []byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("csrf", csrf); err != nil {
		t.Fatalf("write csrf: %v", err)
	}
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest("POST", target, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
