package webdavsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/webdavsvc"
)

// TestPutDeleteGuardHiddenCard pins the C2.4 rule for PUT and DELETE. A card
// invisible to a device token must behave as absent. The card here lives only in
// a primary-only book and the token is secondary. Before the guard, that token
// could overwrite or delete the card even though GET already returned 404.
func TestPutDeleteGuardHiddenCard(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	users := store.NewUsers(pool)
	u, _, err := users.Signup(ctx, testUsername, testEmail, "Nick", testPassword, 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	owner := model.Actor{UserID: u.ID, Username: testUsername}

	// A primary-only book holding one card that is tagged nowhere else.
	var workID int64
	if err := store.WithTx(ctx, pool, owner, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, u.ID); err != nil {
			return err
		}
		b, err := s.CreateBook(ctx, "work", "Work", nil, 200)
		if err != nil {
			return err
		}
		workID = b.ID
		return s.SetBookTiers(ctx, b.ID, []string{"primary"})
	}); err != nil {
		t.Fatalf("create primary-only book: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, 'hidden.vcf', 'urn:uuid:hidden', $2, '{}', 'etag')`,
		u.ID, "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:hidden\r\nFN:Hidden\r\nEND:VCARD\r\n"); err != nil {
		t.Fatalf("insert hidden card: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO contact_books (contact_id, book_id)
		 SELECT id, $2 FROM contacts WHERE user_id = $1 AND filename = 'hidden.vcf'`,
		u.ID, workID); err != nil {
		t.Fatalf("tag hidden card: %v", err)
	}

	_, primaryTok, err := store.NewPrincipals(pool).IssueToken(ctx, u.ID, "primary", "laptop")
	if err != nil {
		t.Fatalf("issue primary token: %v", err)
	}
	_, secondaryTok, err := store.NewPrincipals(pool).IssueToken(ctx, u.ID, "secondary", "phone")
	if err != nil {
		t.Fatalf("issue secondary token: %v", err)
	}

	ts := httptest.NewServer(webdavsvc.New(users, pool))
	defer ts.Close()
	secondary := bearerHTTPClient("Bearer " + secondaryTok)
	primary := bearerHTTPClient("Bearer " + primaryTok)
	cardURL := ts.URL + "/carddav/" + testUsername + "/contacts/all/hidden.vcf"

	// Secondary cannot see it.
	if code, _ := do(t, secondary, http.MethodGet, cardURL, ""); code != http.StatusNotFound {
		t.Fatalf("secondary GET hidden card = %d, want 404", code)
	}
	// Secondary cannot overwrite it...
	if code, _ := do(t, secondary, http.MethodPut, cardURL, vcardBody("Hidden Hijacked")); code != http.StatusNotFound {
		t.Fatalf("secondary PUT hidden card = %d, want 404", code)
	}
	// ...and cannot delete it.
	if code, _ := do(t, secondary, http.MethodDelete, cardURL, ""); code != http.StatusNotFound {
		t.Fatalf("secondary DELETE hidden card = %d, want 404", code)
	}
	var live int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM contacts WHERE user_id = $1 AND filename = 'hidden.vcf' AND deleted_at IS NULL`,
		u.ID).Scan(&live); err != nil {
		t.Fatalf("count hidden: %v", err)
	}
	if live != 1 {
		t.Fatal("the hidden card was mutated by a device that cannot see it")
	}

	// Primary still sees and can update it.
	if code, _ := do(t, primary, http.MethodGet, cardURL, ""); code != http.StatusOK {
		t.Fatalf("primary GET hidden card = %d, want 200", code)
	}

	// The guard covers visibility, not creation. Secondary can still create a new
	// card, which goes into the system book every tier sees.
	freshURL := ts.URL + "/carddav/" + testUsername + "/contacts/all/fresh.vcf"
	if code, _ := do(t, secondary, http.MethodPut, freshURL, vcardBody("Fresh")); code != http.StatusCreated {
		t.Fatalf("secondary create fresh card = %d, want 201", code)
	}
}

// do issues a raw request through the given (auth-injecting) client.
func do(t *testing.T, hc *http.Client, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s req: %v", method, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "text/vcard")
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("ETag")
}

func vcardBody(fn string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:" + fn + "\r\nFN:" + fn + "\r\nEND:VCARD\r\n"
}
