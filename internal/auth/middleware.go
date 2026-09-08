package auth

import (
	"context"
	"net/http"
	"strings"

	"example.com/segments/internal/model"
	"example.com/segments/internal/store"
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

// RequireDAV authenticates a request as either a device principal (Bearer) or
// an account (Basic). A bearer resolves to exactly one principal, whose tier
// gates visibility. Basic has no principal, so it is the tier-all view.
func RequireDAV(users *store.Users, principals *store.PrincipalStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := resolve(r, users, principals)
		if !ok {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

func resolve(r *http.Request, users *store.Users, principals *store.PrincipalStore) (Actor, bool) {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return bearerActor(r.Context(), users, principals, strings.TrimPrefix(authz, "Bearer "))
	}
	login, password, ok := r.BasicAuth()
	if !ok {
		return Actor{}, false
	}
	u, err := users.ByLogin(r.Context(), login)
	if err != nil || !store.CheckPassword(u, password) {
		return Actor{}, false
	}
	return Actor{UserID: u.ID, Username: u.Username}, true
}

func bearerActor(ctx context.Context, users *store.Users, principals *store.PrincipalStore, raw string) (Actor, bool) {
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
