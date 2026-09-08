package web

import (
	"net/http"
	"strings"

	"github.com/nicolaegr/mnemo/internal/api"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/webdavsvc"
)

type Deps struct {
	Store *store.Store
}

// New wires the whole server. CardDAV serves at the root because webdavsvc's
// paths already start with /carddav and must not be remounted under a prefix.
// The REST API lives under /api/v1 and sees its own routes (the prefix is
// stripped here).
func New(d Deps) http.Handler {
	users := store.NewUsers(d.Store.PG)
	dav := webdavsvc.New(users, d.Store.PG)
	rest := api.New(users, d.Store.PG)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/carddav" {
			http.Redirect(w, r, "/carddav/", http.StatusMovedPermanently)
			return
		}
		if suffix, ok := strings.CutPrefix(r.URL.Path, "/api/v1"); ok {
			path := "/" + strings.TrimPrefix(suffix, "/")
			stripped := r.Clone(r.Context())
			stripped.URL.Path = path
			stripped.URL.RawPath = ""
			rest.ServeHTTP(w, stripped)
			return
		}
		dav.ServeHTTP(w, r)
	})
}
