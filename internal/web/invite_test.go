package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web"
	"github.com/nicolaegr/mnemo/internal/websession"
)

// TestInviteLoginFlow walks the whole link: landing, sign-in, and acceptance.
func TestInviteLoginFlow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	alice, err := users.Signup(ctx, "alice", "alice@example.com", "Alice", "pw", 4)
	if err != nil {
		t.Fatalf("signup alice: %v", err)
	}
	bob, err := users.Signup(ctx, "bob", "bob@example.com", "Bob", "pw", 4)
	if err != nil {
		t.Fatalf("signup bob: %v", err)
	}
	inv, err := store.CreateInvite(ctx, pool, alice.ID, store.InviteNetwork, 0, bob.Email)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	sess := websession.New("session", []byte("test-secret"), time.Hour, false, http.SameSiteLaxMode)
	h := web.New(web.Deps{Store: &store.Store{PG: pool}, Session: sess})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/invite/"+inv.Token, nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("GET /invite = %d %q, want 303 /login", rec.Code, rec.Header().Get("Location"))
	}
	pending := cookieNamed(t, rec.Result().Cookies(), "mnemo_invite")
	if pending == nil || pending.Value != inv.Token {
		t.Fatalf("invite cookie = %+v", pending)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader("username=bob&password=pw"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(pending)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /login = %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Redirect"); got != "/dashboard/invites/"+inv.Token {
		t.Fatalf("X-Redirect = %q, want the invite page", got)
	}
	session := cookieNamed(t, rec.Result().Cookies(), "session")
	if session == nil {
		t.Fatal("no session cookie")
	}

	sessionReq := httptest.NewRequest("GET", "/dashboard", nil)
	sessionReq.AddCookie(session)
	s, ok := sess.Read(sessionReq)
	if !ok {
		t.Fatal("session does not read back")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/dashboard/invites/"+inv.Token+"/accept",
		strings.NewReader("csrf="+url.QueryEscape(s.CSRF)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST accept = %d: %s", rec.Code, rec.Body.String())
	}

	ok, err = store.NetworkHas(ctx, pool, alice.ID, bob.ID)
	if err != nil {
		t.Fatalf("network has: %v", err)
	}
	if !ok {
		t.Fatal("accepting the invite did not connect the two users")
	}
}

func cookieNamed(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
