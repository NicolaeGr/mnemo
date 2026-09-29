// Package websession issues and verifies the signed session cookie the admin
// UI uses. The cookie is stateless (HMAC over a small payload), so nothing has
// to be stored server-side.
package websession

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

// Manager signs and reads session cookies.
type Manager struct {
	Name     string
	Secret   []byte
	TTL      time.Duration
	Secure   bool
	SameSite http.SameSite

	now func() time.Time
}

// Session is the decoded cookie payload.
type Session struct {
	UserID int64
	CSRF   string
	Exp    time.Time
}

type payload struct {
	Sub  int64  `json:"sub"`
	CSRF string `json:"csrf"`
	Exp  int64  `json:"exp"`
}

func New(name string, secret []byte, ttl time.Duration, secure bool, sameSite http.SameSite) *Manager {
	return &Manager{Name: name, Secret: secret, TTL: ttl, Secure: secure, SameSite: sameSite, now: time.Now}
}

// Issue writes a fresh session cookie for userID and returns it.
func (m *Manager) Issue(w http.ResponseWriter, userID int64) (Session, error) {
	csrfRaw := make([]byte, 32)
	if _, err := rand.Read(csrfRaw); err != nil {
		return Session{}, err
	}
	s := Session{UserID: userID, CSRF: base64.RawURLEncoding.EncodeToString(csrfRaw), Exp: m.now().Add(m.TTL)}
	http.SetCookie(w, m.cookie(m.sign(s), int(m.TTL.Seconds())))
	return s, nil
}

// Read returns the session carried by the request, or false when the cookie is
// absent, malformed, tampered with, or expired.
func (m *Manager) Read(r *http.Request) (Session, bool) {
	c, err := r.Cookie(m.Name)
	if err != nil {
		return Session{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return Session{}, false
	}
	return m.verify(raw)
}

// Clear expires the session cookie.
func (m *Manager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, m.cookie("", -1))
}

func (m *Manager) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     m.Name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.Secure,
		SameSite: m.SameSite,
		MaxAge:   maxAge,
	}
}

// sign returns payload || HMAC(payload), base64url-encoded as one token.
func (m *Manager) sign(s Session) string {
	body, _ := json.Marshal(payload{Sub: s.UserID, CSRF: s.CSRF, Exp: s.Exp.Unix()})
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write(body)
	raw := append(body, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (m *Manager) verify(raw []byte) (Session, bool) {
	const sigLen = sha256.Size
	if len(raw) <= sigLen {
		return Session{}, false
	}
	body, sig := raw[:len(raw)-sigLen], raw[len(raw)-sigLen:]
	mac := hmac.New(sha256.New, m.Secret)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Session{}, false
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return Session{}, false
	}
	s := Session{UserID: p.Sub, CSRF: p.CSRF, Exp: time.Unix(p.Exp, 0)}
	if m.now().After(s.Exp) {
		return Session{}, false
	}
	return s, true
}

// CheckCSRF compares the request's CSRF token (header or form field) against the
// value bound to the session.
func CheckCSRF(r *http.Request, s Session) bool {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf")
	}
	return token != "" && hmac.Equal([]byte(token), []byte(s.CSRF))
}
