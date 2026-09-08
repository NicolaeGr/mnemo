package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"example.com/segments/internal/auth"
	"example.com/segments/internal/store"
	"example.com/segments/internal/web/domain"
	"example.com/segments/internal/web/layouts"
	"example.com/segments/internal/web/pages"
	"example.com/segments/internal/web/seg"
	"example.com/segments/internal/web/webdavsvc"
)

type Deps struct {
	Store    *store.Store
	Sessions *auth.SessionManager
}

func New(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("assets"))))

	r.Mount("/carddav", webdavsvc.New(store.NewUsers(d.Store.PG), d.Store.PG))
	r.Handle("/.well-known/carddav", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/carddav/", http.StatusFound)
	}))

	root := seg.Segment{
		ID: "root",
		Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
			return layouts.RootSegment(child)
		},
	}
	marketing := seg.Segment{
		ID: "marketing",
		Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
			return layouts.MarketingSegment(child)
		},
	}
	authSeg := seg.Segment{
		ID: "auth",
		Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
			return layouts.AuthSegment(child)
		},
	}
	dashboard := seg.Segment{
		ID:     "dashboard",
		TTL:    30 * time.Second,
		Scoped: true,
		Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
			return layouts.DashboardSegment(child)
		},
		Load: func(ctx context.Context) (any, error) {
			uid, ok := auth.UserIDFrom(ctx)
			if !ok {
				return nil, http.ErrNoCookie
			}
			u, err := store.NewUsers(d.Store.PG).ByID(ctx, uid)
			if err != nil {
				return nil, err
			}
			return &domain.User{Name: u.Name, Email: u.Email, Notifs: 3}, nil
		},
		Decode: func(b []byte) (any, error) {
			var u domain.User
			if err := json.Unmarshal(b, &u); err != nil {
				return nil, err
			}
			return &u, nil
		},
	}
	settings := seg.Segment{
		ID:     "settings",
		TTL:    5 * time.Minute,
		Scoped: true,
		Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
			return layouts.SettingsSegment(child)
		},
		Load: func(ctx context.Context) (any, error) { return &domain.BadgeCounts{N: 7}, nil },
		Decode: func(b []byte) (any, error) {
			var c domain.BadgeCounts
			if err := json.Unmarshal(b, &c); err != nil {
				return nil, err
			}
			return &c, nil
		},
	}

	// Logout must be reachable without the dashboard subtree.
	r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
		d.Sessions.Destroy(w, r)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})

	r.Route("/", func(r chi.Router) {
		r.Use(seg.Use(root))

		r.Group(func(r chi.Router) {
			r.Use(seg.Use(marketing))
			r.Get("/", func(w http.ResponseWriter, r *http.Request) {
				seg.Page(w, r, "Home", pages.Home())
			})
			r.Get("/about", func(w http.ResponseWriter, r *http.Request) {
				seg.Page(w, r, "About", pages.About())
			})
		})

		r.Group(func(r chi.Router) {
			r.Use(seg.Use(authSeg))
			r.Post("/login", handleLogin(d))
			r.Get("/login", func(w http.ResponseWriter, r *http.Request) {
				if _, err := d.Sessions.UserID(r); err == nil {
					http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
					return
				}
				seg.Page(w, r, "Sign in", pages.Login())
			})
		})

		r.Route("/dashboard", func(r chi.Router) {
			r.Use(d.Sessions.RequireAuth)
			r.Use(seg.Use(dashboard))

			r.Get("/", func(w http.ResponseWriter, r *http.Request) {
				seg.Page(w, r, "Overview", pages.DashHome())
			})

			r.Post("/notifications/read", func(w http.ResponseWriter, r *http.Request) {
				sid := seg.SessionID(w, r)
				seg.Invalidate("dashboard", sid)
				w.Header().Set("HX-Trigger", `showToast`)
				_ = seg.NotifBadge(0).Render(r.Context(), w)
			})

			r.Route("/settings", func(r chi.Router) {
				r.Use(seg.Use(settings))

				shell := func(ctx context.Context, opts seg.ModalOpts, owner, current, parent string, child templ.Component) templ.Component {
					return layouts.ModalShell(opts, owner, current, parent, child)
				}
				oob := []templ.Component{layouts.SettingsTabsOOB()}

				r.Get("/", seg.Modalable(shell, pages.SettingsLeaf, pages.SettingsModalOpts(), oob...))
				r.Get("/notifications", seg.Modalable(shell, pages.NotificationsLeaf, pages.SettingsModalOpts(), oob...))
				r.Get("/billing", seg.Modalable(shell, pages.BillingLeaf, pages.SettingsModalOpts(), oob...))
			})
		})

		r.Post("/billing/export", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("HX-Trigger", `showToast`)
			w.WriteHeader(http.StatusOK)
		})
	})

	return r
}

func handleLogin(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		u, err := store.NewUsers(d.Store.PG).ByLogin(r.Context(), r.FormValue("email"))
		if err != nil || !store.CheckPassword(u, r.FormValue("password")) {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}
		if err := d.Sessions.Create(w, u.ID); err != nil {
			http.Error(w, "could not start session", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/dashboard")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	}
}
