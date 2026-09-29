package websession

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testManager() *Manager {
	return New("session", []byte("test-secret-key"), time.Hour, false, http.SameSiteLaxMode)
}

func issueCookie(t *testing.T, m *Manager, w http.ResponseWriter, userID int64) Session {
	t.Helper()
	s, err := m.Issue(w, userID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return s
}

func requestWithCookie(value string) *http.Request {
	r := httptest.NewRequest("GET", "/ui/", nil)
	if value != "" {
		r.AddCookie(&http.Cookie{Name: "session", Value: value})
	}
	return r
}

func TestSessionRoundTrip(t *testing.T) {
	m := testManager()
	rec := httptest.NewRecorder()
	s := issueCookie(t, m, rec, 42)

	cookie := rec.Result().Cookies()[0]
	got, ok := m.Read(requestWithCookie(cookie.Value))
	if !ok {
		t.Fatal("valid cookie did not read")
	}
	if got.UserID != 42 {
		t.Fatalf("userID = %d, want 42", got.UserID)
	}
	if got.CSRF == "" || got.CSRF != s.CSRF {
		t.Fatalf("csrf = %q, want %q", got.CSRF, s.CSRF)
	}
}

func TestSessionRejectsTamperingAndExpiry(t *testing.T) {
	m := testManager()
	rec := httptest.NewRecorder()
	issueCookie(t, m, rec, 7)
	value := rec.Result().Cookies()[0].Value

	// Flip the last character of the token to corrupt the signature.
	corrupt := value[:len(value)-1] + "X"
	if corrupt == value {
		corrupt = value[:len(value)-1] + "Y"
	}
	if _, ok := m.Read(requestWithCookie(corrupt)); ok {
		t.Fatal("corrupted cookie was accepted")
	}
	if _, ok := m.Read(requestWithCookie("")); ok {
		t.Fatal("missing cookie was accepted")
	}

	// A different secret must not verify a cookie signed by m.
	other := New("session", []byte("different-secret"), time.Hour, false, http.SameSiteLaxMode)
	if _, ok := other.Read(requestWithCookie(value)); ok {
		t.Fatal("cookie verified under the wrong secret")
	}

	// Past expiry the cookie is rejected.
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, ok := m.Read(requestWithCookie(value)); ok {
		t.Fatal("expired cookie was accepted")
	}
}

func TestCheckCSRF(t *testing.T) {
	m := testManager()
	rec := httptest.NewRecorder()
	s := issueCookie(t, m, rec, 1)

	header := httptest.NewRequest("POST", "/ui/books", nil)
	header.Header.Set("X-CSRF-Token", s.CSRF)
	if !CheckCSRF(header, s) {
		t.Fatal("matching header token rejected")
	}

	form := httptest.NewRequest("POST", "/ui/books",
		strings.NewReader(url.Values{"csrf": {s.CSRF}}.Encode()))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !CheckCSRF(form, s) {
		t.Fatal("matching form token rejected")
	}

	wrong := httptest.NewRequest("POST", "/ui/books", nil)
	wrong.Header.Set("X-CSRF-Token", "nope")
	if CheckCSRF(wrong, s) {
		t.Fatal("wrong token accepted")
	}
	if CheckCSRF(httptest.NewRequest("POST", "/ui/books", nil), s) {
		t.Fatal("absent token accepted")
	}
}
