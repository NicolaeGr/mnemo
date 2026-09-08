package webdavsvc

import (
	"net/http"

	"github.com/emersion/go-webdav/carddav"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/auth"
	"github.com/nicolaegr/mnemo/internal/store"
)

// New builds the CardDAV handler for the whole DAV plane. Identity is resolved
// per request from a Bearer principal token or a Basic credential; no userID is
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

	// Root mount: answers PROPFIND / with current-user-principal.
	mux.Handle("/", &carddav.Handler{Backend: b, Prefix: prefix})
	// Principal + home-set + address-book + objects.
	mux.Handle(prefix+"/", &carddav.Handler{Backend: b, Prefix: prefix})

	return auth.RequireDAV(users, store.NewPrincipals(pool), &davFilter{next: mux, b: b})
}
