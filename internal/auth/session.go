package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"
)

type ctxKey string

const (
	SessionCookieName = "mnemo_session"
	sessionDuration   = 24 * time.Hour
)

type Session struct {
	UserID    string
	Username  string
	Role      string
	ExpiresAt time.Time
}

type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[string]*Session),
	}
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (ss *SessionStore) Create(userID, username, role string) (string, *Session, error) {
	token, err := generateToken()
	if err != nil {
		return "", nil, err
	}

	sess := &Session{
		UserID:    userID,
		Username:  username,
		Role:      role,
		ExpiresAt: time.Now().Add(sessionDuration),
	}

	ss.mu.Lock()
	ss.sessions[token] = sess
	ss.mu.Unlock()

	return token, sess, nil
}

func (ss *SessionStore) Get(token string) *Session {
	ss.mu.RLock()
	sess, ok := ss.sessions[token]
	ss.mu.RUnlock()

	if !ok {
		return nil
	}

	if time.Now().After(sess.ExpiresAt) {
		ss.Delete(token)
		return nil
	}

	return sess
}

func (ss *SessionStore) Delete(token string) {
	ss.mu.Lock()
	delete(ss.sessions, token)
	ss.mu.Unlock()
}

type userCtxKey struct{}

func ContextWithUser(ctx context.Context, sess *Session) context.Context {
	return context.WithValue(ctx, userCtxKey{}, sess)
}

func UserFromContext(ctx context.Context) *Session {
	sess, _ := ctx.Value(userCtxKey{}).(*Session)
	return sess
}

func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionDuration),
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func GetSessionToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) {
			return "", nil
		}
		return "", err
	}
	if cookie.Value == "" {
		return "", nil
	}
	return cookie.Value, nil
}
