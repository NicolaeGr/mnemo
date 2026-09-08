package webdavsvc

import (
	"net/http"

	"github.com/emersion/go-webdav/carddav"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/auth"
	"example.com/segments/internal/store"
)

// New builds the CardDAV handler for the whole DAV plane. Identity is resolved
// per request from the Basic credential (auth.RequireBasic); no userID is
// injected at construction time.
//
// The go-webdav client PROPFINDs the server root ("/") for current-user-principal,
// so the root must also be answered by a CardDAV handler. Both mounts share one
// backend; the library derives resource type from PATH DEPTH relative to Prefix,
// so the principal mount lives at /carddav/{username}/ (depth 1) with the
// address-book home set one level below (see backend.go).
func New(users *store.Users, pool *pgxpool.Pool) http.Handler {
	mux := http.NewServeMux()
	b := &backend{users: users, pool: pool}

	mux.HandleFunc("/.well-known/carddav", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.ActorFrom(r.Context())
		if !ok {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, principalPath(actor.Username), http.StatusFound)
	})

	// Root mount: answers PROPFIND / with current-user-principal.
	mux.Handle("/", &carddav.Handler{Backend: b, Prefix: prefix})
	// Principal + home-set + address-book + objects.
	mux.Handle(prefix+"/", &carddav.Handler{Backend: b, Prefix: prefix})

	return auth.RequireBasic(users, mux)
}
