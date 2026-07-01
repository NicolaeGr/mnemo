package server

import (
	"context"
	"net/http"

	"github.com/a-h/templ"

	"mnemo/internal/auth"
	"mnemo/internal/views"
)

type ctxKey string

const (
	IsHTMXKey ctxKey = "is_htmx"
)

func HTMXMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isHTMX := r.Header.Get("HX-Request") == "true"
		ctx := context.WithValue(r.Context(), IsHTMXKey, isHTMX)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func IsHTMX(r *http.Request) bool {
	return r.Context().Value(IsHTMXKey).(bool)
}

func usernameFromContext(r *http.Request) string {
	sess := auth.UserFromContext(r.Context())
	if sess != nil {
		return sess.Username
	}
	return ""
}

func RenderPage(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	username := usernameFromContext(r)
	if IsHTMX(r) {
		views.PageWrapper(content).Render(r.Context(), w)
	} else {
		views.AppLayout(title, username, content).Render(r.Context(), w)
	}
}

func RenderSettingsPage(w http.ResponseWriter, r *http.Request, title string, content templ.Component) {
	username := usernameFromContext(r)
	if IsHTMX(r) {
		views.PageWrapper(content).Render(r.Context(), w)
	} else {
		views.SettingsLayout(title, username, content).Render(r.Context(), w)
	}
}
