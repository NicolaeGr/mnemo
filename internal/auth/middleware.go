package auth

import (
	"context"
	"net/http"

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

func RequireBasic(users *store.Users, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok {
			unauthorized(w)
			return
		}
		u, err := users.ByLogin(r.Context(), username)
		if err != nil || !store.CheckPassword(u, password) {
			unauthorized(w)
			return
		}
		actor := Actor{UserID: u.ID, Username: u.Username}
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="contacts", charset="UTF-8"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
