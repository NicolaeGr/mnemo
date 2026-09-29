// Package admin serves the htmx admin UI: session-cookie login, then a
// dashboard behind the seg segment stack. It talks to the store directly, the
// same way the REST handlers do, so the UI never round-trips over HTTP to
// itself.
package admin

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/store"
	"github.com/nicolaegr/mnemo/internal/web/domain"
	"github.com/nicolaegr/mnemo/internal/web/layouts"
	"github.com/nicolaegr/mnemo/internal/web/pages"
	"github.com/nicolaegr/mnemo/internal/web/seg"
	"github.com/nicolaegr/mnemo/internal/web/uictx"
	"github.com/nicolaegr/mnemo/internal/websession"
)

type Admin struct {
	users   *store.Users
	pool    *pgxpool.Pool
	session *websession.Manager
}

// New builds the admin handler. Paths are absolute (/login, /dashboard, …) and
// the root router sends matching requests here.
func New(users *store.Users, pool *pgxpool.Pool, session *websession.Manager) http.Handler {
	a := &Admin{users: users, pool: pool, session: session}
	mux := http.NewServeMux()

	mux.Handle("GET /login", a.authStack(http.HandlerFunc(a.loginPage)))
	mux.Handle("POST /login", http.HandlerFunc(a.loginSubmit))
	mux.Handle("POST /logout", a.requireCSRF(http.HandlerFunc(a.logout)))

	mux.Handle("GET /dashboard", a.dashPage("Overview", a.overviewLeaf))
	mux.Handle("GET /dashboard/settings", a.dashPage("Settings", a.settingsLeaf))

	return mux
}

var rootSeg = seg.Segment{
	ID: "root",
	Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
		return layouts.RootSegment(child)
	},
}

var authSeg = seg.Segment{
	ID: "auth",
	Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
		return layouts.AuthSegment(child)
	},
}

var dashSeg = seg.Segment{
	ID: "dashboard",
	Render: func(ctx context.Context, _ any, child templ.Component) templ.Component {
		return layouts.DashboardSegment(child)
	},
	Load: func(ctx context.Context) (any, error) {
		s := uictx.From(ctx)
		return &domain.User{Name: s.Name, Email: s.Email}, nil
	},
}

// authStack is the public login stack (root + auth), no session required.
func (a *Admin) authStack(next http.Handler) http.Handler {
	return seg.Use(rootSeg)(seg.Use(authSeg)(next))
}

// dashPage wraps a leaf in the session guard and the dashboard segment.
func (a *Admin) dashPage(title string, leaf func(r *http.Request) templ.Component) http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg.Page(w, r, title, leaf(r))
	})
	return a.requireSession(seg.Use(dashSeg)(h))
}

func (a *Admin) loginPage(w http.ResponseWriter, r *http.Request) {
	seg.Page(w, r, "Sign in", pages.Login())
}

// loginSubmit verifies credentials and issues the session cookie. The login
// form posts via fetch and navigates on 200.
func (a *Admin) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u, err := a.users.ByLogin(r.Context(), r.FormValue("username"))
	if err != nil || !store.CheckPassword(u, r.FormValue("password")) {
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}
	if _, err := a.session.Issue(w, u.ID); err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *Admin) logout(w http.ResponseWriter, r *http.Request) {
	a.session.Clear(w)
	if isHX(r) {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *Admin) overviewLeaf(r *http.Request) templ.Component {
	var books, devices, contacts int
	_ = a.pool.QueryRow(r.Context(), `
		SELECT (SELECT count(*) FROM books WHERE owner_user_id = $1 AND is_active),
		       (SELECT count(*) FROM principals WHERE user_id = $1),
		       (SELECT count(*) FROM contacts WHERE user_id = $1 AND deleted_at IS NULL)`,
		uictx.From(r.Context()).UserID).Scan(&books, &devices, &contacts)
	return pages.Overview(books, devices, contacts)
}

func (a *Admin) settingsLeaf(r *http.Request) templ.Component {
	return pages.SettingsIndex(uictx.From(r.Context()))
}

// requireSession loads the session, resolves the account, and puts both into
// the request context for templates. Missing or stale sessions go to /login.
func (a *Admin) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.session.Read(r)
		if !ok {
			a.toLogin(w, r)
			return
		}
		u, err := a.users.ByID(r.Context(), sess.UserID)
		if err != nil {
			a.toLogin(w, r)
			return
		}
		ctx := uictx.With(r.Context(), uictx.Session{
			UserID: u.ID, Username: u.Username, Name: u.Name, Email: u.Email, CSRF: sess.CSRF,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireCSRF guards cookie-authenticated mutations.
func (a *Admin) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.session.Read(r)
		if !ok {
			a.toLogin(w, r)
			return
		}
		if !websession.CheckCSRF(r, sess) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Admin) toLogin(w http.ResponseWriter, r *http.Request) {
	if isHX(r) {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func isHX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}
