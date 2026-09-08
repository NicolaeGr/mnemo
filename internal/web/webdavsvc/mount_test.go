package webdavsvc_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web"
)

// The full app router serves CardDAV at the root, so /carddav/{user}/...
// reaches webdavsvc with its prefix intact. A phone's address-book PROPFIND
// must get the ctag shim's answer.
func TestAppServesCarddavAtRoot(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, _, err := users.Signup(ctx, testUsername, testEmail, "Nick", testPassword, bcrypt.DefaultCost); err != nil {
		t.Fatalf("signup: %v", err)
	}

	handler := web.New(web.Deps{Store: &store.Store{PG: pool}})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ab := "/carddav/" + testUsername + "/contacts/all/"
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/></d:prop></d:propfind>`
	req, err := http.NewRequest("PROPFIND", ts.URL+ab, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("req: %v", err)
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")
	req.SetBasicAuth(testUsername, testPassword)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("propfind: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("address book PROPFIND = %d, want 207. body:\n%s", resp.StatusCode, buf.String())
	}
	if !strings.Contains(buf.String(), "getctag") {
		t.Fatalf("response missing getctag:\n%s", buf.String())
	}
}
