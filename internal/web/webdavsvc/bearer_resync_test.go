package webdavsvc_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/emersion/go-webdav/carddav"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/webdavsvc"
)

// TestTierFlipChangesDeviceViewAndCtag pins the DAV-side consequence of a tier
// change: a device only syncs the books its tier can see, and an epoch bump
// invalidates its sync token (visible as a changed getctag). A card living only
// in a primary-only book must vanish from a device demoted to secondary.
func TestTierFlipChangesDeviceViewAndCtag(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	u, _, err := users.Signup(ctx, testUsername, testEmail, "Nick", testPassword, 4)
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	owner := model.Actor{UserID: u.ID, Username: testUsername}
	pr, raw, err := store.NewPrincipals(pool).IssueToken(ctx, u.ID, "primary", "phone")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	authz := "Bearer " + raw

	// A primary-only book, then a card tagged into it and nowhere else.
	var workID int64
	err = store.WithTx(ctx, pool, owner, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, u.ID); err != nil {
			return err
		}
		b, err := s.CreateBook(ctx, "work", "Work", nil, 200)
		if err != nil {
			return err
		}
		workID = b.ID
		return s.SetBookTiers(ctx, b.ID, []string{"primary"})
	})
	if err != nil {
		t.Fatalf("create primary-only book: %v", err)
	}
	workCard := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:urn:uuid:work-1\r\nFN:Work Card\r\nEND:VCARD\r\n"
	if _, err := pool.Exec(ctx, `
		INSERT INTO contacts (user_id, filename, uid, vcard_text, search_meta, etag)
		VALUES ($1, 'workcard.vcf', 'urn:uuid:work-1', $2, '{}', 'deadbeef')`,
		u.ID, workCard); err != nil {
		t.Fatalf("insert work card: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO contact_books (contact_id, book_id)
		 SELECT id, $2 FROM contacts WHERE user_id = $1 AND filename = 'workcard.vcf'`,
		u.ID, workID); err != nil {
		t.Fatalf("tag work card: %v", err)
	}

	ts := httptest.NewServer(webdavsvc.New(users, pool))
	defer ts.Close()
	client, err := carddav.NewClient(bearerHTTPClient(authz), ts.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	books, err := client.FindAddressBooks(ctx, "/carddav/"+testUsername+"/contacts/")
	if err != nil {
		t.Fatalf("find books: %v", err)
	}
	book := books[0].Path

	// As primary the device sees the work card.
	before, err := client.QueryAddressBook(ctx, book, &carddav.AddressBookQuery{})
	if err != nil {
		t.Fatalf("query before: %v", err)
	}
	if len(before) != 1 || !strings.HasSuffix(before[0].Path, "workcard.vcf") {
		t.Fatalf("primary device view = %+v, want [workcard.vcf]", before)
	}
	ctagBefore := getCtag(t, ts, book, authz)

	// Demote the device to secondary: it loses the primary-only book and its epoch
	// bumps, so its view and getctag both change.
	if err := store.WithTx(ctx, pool, owner, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, u.ID); err != nil {
			return err
		}
		return s.SetPrincipalTier(ctx, pr.ID, "secondary")
	}); err != nil {
		t.Fatalf("demote device: %v", err)
	}

	after, err := client.QueryAddressBook(ctx, book, &carddav.AddressBookQuery{})
	if err != nil {
		t.Fatalf("query after: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("secondary device view = %+v, want no cards", after)
	}
	ctagAfter := getCtag(t, ts, book, authz)
	if ctagAfter == ctagBefore {
		t.Fatalf("getctag unchanged across tier flip (%q)", ctagAfter)
	}
}

// bearerHTTPClient injects a fixed Authorization header on every request.
func bearerHTTPClient(authz string) *http.Client {
	base := http.DefaultTransport
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r := req.Clone(req.Context())
		r.Header.Set("Authorization", authz)
		return base.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// getCtag issues a Depth:0 PROPFIND on the address book and returns the getctag
// value it carries.
func getCtag(t *testing.T, ts *httptest.Server, path, authz string) string {
	t.Helper()
	req, err := http.NewRequest("PROPFIND", ts.URL+path, nil)
	if err != nil {
		t.Fatalf("propfind req: %v", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Authorization", authz)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read propfind: %v", err)
	}
	open, closeTag := "<cs:getctag>", "</cs:getctag>"
	start := strings.Index(string(body), open)
	if start < 0 {
		t.Fatalf("no getctag in PROPFIND response: %s", body)
	}
	rest := string(body)[start+len(open):]
	return rest[:strings.Index(rest, closeTag)]
}
