package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

// Actor aliases model.Actor so auth's API keeps reading auth.Actor at call
// sites while store can depend on model without an import cycle.
type Actor = model.Actor

type actorKey struct{}

// WithActor stores the resolved actor in the request context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor resolved by the auth middleware.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Option configures credential admission.
type Option func(*options)

type options struct {
	allowTokenPassword bool
}

// WithTokenPassword lets a device token be presented as the Basic password.
// Real CardDAV clients cannot send a Bearer header, so they need this to reach a
// tier-gated view. Opt-in, since the token then travels in a cleartext header.
func WithTokenPassword(enabled bool) Option {
	return func(o *options) { o.allowTokenPassword = enabled }
}

func optionsFrom(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// RequireDAV authenticates as a device principal (Bearer, or a token as the
// Basic password when enabled) or as an account (Basic username and password).
// An account has no principal and sees every tier; a principal's tier gates it.
func RequireDAV(users *store.Users, principals *store.PrincipalStore, next http.Handler, opts ...Option) http.Handler {
	o := optionsFrom(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		now := time.Now()
		if !authFailures.allowed(ip, now) {
			tooManyRequests(w)
			return
		}
		actor, ok := resolve(r, users, principals, o)
		if !ok {
			authFailures.fail(ip, now)
			unauthorized(w)
			return
		}
		authFailures.clear(ip)
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

// authFailures caps auth-failure bursts per IP (10/min).
var authFailures = newFailLimiter(10, time.Minute)

func resolve(r *http.Request, users *store.Users, principals *store.PrincipalStore, o options) (Actor, bool) {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return principalActor(r.Context(), users, principals, strings.TrimPrefix(authz, "Bearer "))
	}
	login, password, ok := r.BasicAuth()
	if !ok {
		return Actor{}, false
	}
	if u, err := users.ByLogin(r.Context(), login); err == nil && store.CheckPassword(u, password) {
		return Actor{UserID: u.ID, Username: u.Username}, true
	}
	// Token presented as the password, tried only after the account check and
	// only when enabled.
	if o.allowTokenPassword {
		return principalActor(r.Context(), users, principals, password)
	}
	return Actor{}, false
}

func principalActor(ctx context.Context, users *store.Users, principals *store.PrincipalStore, raw string) (Actor, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Actor{}, false
	}
	p, err := principals.ByTokenHash(ctx, store.TokenHashOf(raw))
	if err != nil {
		return Actor{}, false
	}
	u, err := users.ByID(ctx, p.UserID)
	if err != nil {
		return Actor{}, false
	}
	_ = principals.Touch(ctx, p.ID)
	id := p.ID
	return Actor{UserID: p.UserID, Username: u.Username, PrincipalID: &id}, true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="contacts", charset="UTF-8"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
