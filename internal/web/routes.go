package web

import (
	"net/http"

	"github.com/nicolaegr/mnemo/internal/api"
	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/webdavsvc"
)

type Deps struct {
	Store *store.Store
}

// New wires the whole server: CardDAV at the root (webdavsvc paths start with
// /carddav and must not be remounted under a prefix), the REST API under
// /api/v1, and the well-known discovery redirect.
func New(d Deps) http.Handler {
	users := store.NewUsers(d.Store.PG)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/carddav", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/carddav/", http.StatusMovedPermanently)
	})
	mux.Handle("/api/v1/", api.New(users, d.Store.PG))
	mux.Handle("/", webdavsvc.New(users, d.Store.PG))
	return mux
}
