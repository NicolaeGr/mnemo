package auth

import (
	"net/http"

	"mnemo/internal/services"
)

type Middleware struct {
	Sessions    *SessionStore
	UserService *services.UserService
}

func NewMiddleware(sessions *SessionStore, userSvc *services.UserService) *Middleware {
	return &Middleware{Sessions: sessions, UserService: userSvc}
}

func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := GetSessionToken(r)
		if err != nil || token == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		sess := m.Sessions.Get(token)
		if sess == nil {
			ClearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		ctx := ContextWithUser(r.Context(), sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Middleware) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := UserFromContext(r.Context())
		if sess == nil || sess.Role != "admin" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) RedirectIfAuthed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := GetSessionToken(r)
		if err == nil && token != "" {
			if sess := m.Sessions.Get(token); sess != nil {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) RequireSetup(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count, err := m.UserService.CountUsers(r.Context())
		if err != nil {
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		if count == 0 {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
