package web

import (
	"net/http"
	"strings"

	"github.com/nicolaegr/mnemo/internal/api"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/admin"
	"github.com/nicolaegr/mnemo/internal/web/webdavsvc"
	"github.com/nicolaegr/mnemo/internal/websession"
)

type Deps struct {
	Store   *store.Store
	Session *websession.Manager
}

// New wires the whole server. CardDAV serves at the root because webdavsvc's
// paths already start with /carddav and must not be remounted under a prefix.
// The REST API lives under /api/v1 and the admin UI at /login + /dashboard;
// both see their own routes after the prefix is chosen here.
func New(d Deps) http.Handler {
	users := store.NewUsers(d.Store.PG)
	dav := webdavsvc.New(users, d.Store.PG)
	rest := api.New(users, d.Store.PG)
	ui := admin.New(users, d.Store.PG, d.Session)
	static := http.FileServer(http.Dir("assets"))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/.well-known/carddav":
			http.Redirect(w, r, "/carddav/", http.StatusMovedPermanently)
		case strings.HasPrefix(r.URL.Path, "/static/"):
			http.StripPrefix("/static/", static).ServeHTTP(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/":
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		case isUIPath(r.URL.Path):
			ui.ServeHTTP(w, r)
		default:
			if suffix, ok := strings.CutPrefix(r.URL.Path, "/api/v1"); ok {
				path := "/" + strings.TrimPrefix(suffix, "/")
				stripped := r.Clone(r.Context())
				stripped.URL.Path = path
				stripped.URL.RawPath = ""
				rest.ServeHTTP(w, stripped)
				return
			}
			dav.ServeHTTP(w, r)
		}
	})
}

func isUIPath(p string) bool {
	switch p {
	case "/login", "/logout", "/dashboard":
		return true
	}
	return strings.HasPrefix(p, "/dashboard/")
}
