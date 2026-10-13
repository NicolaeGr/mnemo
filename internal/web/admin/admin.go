// Package admin serves the htmx admin UI: session-cookie login, then a
// dashboard behind the seg segment stack. It talks to the store directly, the
// same way the REST handlers do, so the UI never round-trips over HTTP to
// itself. Add/edit forms open in a modal (#modal-root); their responses refresh
// the page list and clear the modal.
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/auth"
	"github.com/nicolaegr/mnemo/internal/contact"
	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
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
	mux.Handle("POST /logout", a.mutate(http.HandlerFunc(a.logout)))
	mux.Handle("GET /dashboard/modal/close", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	mux.Handle("GET /dashboard", a.dashPage("Overview", a.overviewLeaf))

	mux.Handle("GET /dashboard/books", a.dashPage("Lists", a.booksLeaf))
	mux.Handle("GET /dashboard/books/slug", a.requireSession(http.HandlerFunc(a.bookSlugCheck)))
	mux.Handle("GET /dashboard/books/grid", a.requireSession(http.HandlerFunc(a.booksGrid)))
	mux.Handle("GET /dashboard/books/new", a.modal(a.bookAddModal))
	mux.Handle("GET /dashboard/books/{id}/edit", a.modal(a.bookEditModal))
	mux.Handle("POST /dashboard/books", a.mutate(http.HandlerFunc(a.createBook)))
	mux.Handle("POST /dashboard/books/{id}/edit", a.mutate(http.HandlerFunc(a.editBook)))
	mux.Handle("POST /dashboard/books/{id}/delete", a.mutate(http.HandlerFunc(a.deleteBook)))

	mux.Handle("GET /dashboard/devices", a.dashPage("Devices", a.devicesLeaf))
	mux.Handle("GET /dashboard/devices/rows", a.requireSession(http.HandlerFunc(a.devicesRows)))
	mux.Handle("GET /dashboard/devices/new", a.modal(a.deviceAddModal))
	mux.Handle("GET /dashboard/devices/{id}/edit", a.modal(a.deviceEditModal))
	mux.Handle("POST /dashboard/devices", a.mutate(http.HandlerFunc(a.createDevice)))
	mux.Handle("POST /dashboard/devices/{id}/edit", a.mutate(http.HandlerFunc(a.editDevice)))
	mux.Handle("POST /dashboard/devices/{id}/delete", a.mutate(http.HandlerFunc(a.deleteDevice)))

	mux.Handle("GET /dashboard/contacts", a.dashPage("Contacts", a.contactsLeaf))
	mux.Handle("GET /dashboard/contacts/search", a.requireSession(http.HandlerFunc(a.searchContacts)))
	mux.Handle("GET /dashboard/contacts/new", a.modal(a.contactAddModal))
	mux.Handle("GET /dashboard/contacts/bulk-modal", a.modal(a.bulkListsModal))
	mux.Handle("GET /dashboard/contacts/{id}/view", a.modal(a.contactViewModal))
	mux.Handle("GET /dashboard/contacts/{id}/edit", a.modal(a.contactEditModal))
	mux.Handle("POST /dashboard/contacts", a.mutate(http.HandlerFunc(a.createContact)))
	mux.Handle("POST /dashboard/contacts/bulk", a.mutate(http.HandlerFunc(a.bulkContacts)))
	mux.Handle("POST /dashboard/contacts/{id}/edit", a.mutate(http.HandlerFunc(a.editContact)))
	mux.Handle("POST /dashboard/contacts/{id}/delete", a.mutate(http.HandlerFunc(a.deleteContact)))

	mux.Handle("GET /dashboard/settings", a.dashPage("Settings", a.settingsLeaf))
	mux.Handle("GET /dashboard/settings/modal", a.modal(a.settingsModal))
	mux.Handle("POST /dashboard/settings", a.mutate(http.HandlerFunc(a.saveSettings)))

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

// dashPage wraps a leaf in the session guard, the root shell, and the dashboard
// segment. Root must be in the stack or the full page renders without <head>
// (and its stylesheet).
func (a *Admin) dashPage(title string, leaf func(r *http.Request) templ.Component) http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg.PageWith(w, r, title, leaf(r), layouts.SidebarNavOOB(), layouts.HeaderTitleOOB(title))
	})
	return a.requireSession(seg.Use(rootSeg)(seg.Use(dashSeg)(h)))
}

// modal renders a form shell into #modal-root (the caller targets it).
func (a *Admin) modal(leaf func(r *http.Request) (string, templ.Component)) http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		title, body := leaf(r)
		renderTempl(w, r, layouts.Modal(title, body))
	})
	return a.requireSession(h)
}

func (a *Admin) loginPage(w http.ResponseWriter, r *http.Request) {
	seg.Page(w, r, "Sign in", pages.Login())
}

// loginSubmit verifies credentials and issues the session cookie. The login
// form posts via fetch and navigates on 200.
func (a *Admin) loginSubmit(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r)
	now := time.Now()
	if !auth.AllowAuthAttempt(ip, now) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u, err := a.users.ByLogin(r.Context(), r.FormValue("username"))
	if err != nil || !store.CheckPassword(u, r.FormValue("password")) {
		auth.NoteAuthFailure(ip, now)
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}
	auth.ClearAuthFailures(ip)
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
	ctx := r.Context()
	userID := uictx.From(ctx).UserID
	var books, devices, contacts int
	err := a.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM books WHERE owner_user_id = $1 AND is_active),
		       (SELECT count(*) FROM principals WHERE user_id = $1),
		       (SELECT count(*) FROM contacts WHERE user_id = $1 AND deleted_at IS NULL)`,
		userID).Scan(&books, &devices, &contacts)
	if err != nil {
		return pages.ErrorBanner(err.Error())
	}
	activity, err := a.recentActivity(ctx, userID, 8)
	if err != nil {
		return pages.ErrorBanner(err.Error())
	}
	statuses, err := a.deviceStatuses(ctx, userID)
	if err != nil {
		return pages.ErrorBanner(err.Error())
	}
	return pages.Overview(pages.OverviewData{
		Contacts:       contacts,
		Lists:          books,
		Devices:        devices,
		Activity:       activity,
		DeviceStatuses: statuses,
	})
}

// recentActivity reads the per-user change log, naming each changed contact.
func (a *Admin) recentActivity(ctx context.Context, userID int64, limit int) ([]pages.ActivityItem, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT coalesce(c.search_meta->>'fn', cc.filename), cc.change_type, cc.changed_at
		  FROM contact_changes cc
		  LEFT JOIN contacts c ON c.user_id = cc.user_id AND c.filename = cc.filename
		 WHERE cc.user_id = $1
		 ORDER BY cc.id DESC
		 LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]pages.ActivityItem, 0, limit)
	for rows.Next() {
		var name, kind string
		var at time.Time
		if err := rows.Scan(&name, &kind, &at); err != nil {
			return nil, err
		}
		items = append(items, pages.ActivityItem{Name: name, Kind: activityVerb(kind), At: at})
	}
	return items, rows.Err()
}

func activityVerb(kind string) string {
	if kind == "delete" {
		return "removed"
	}
	return "updated"
}

func (a *Admin) deviceStatuses(ctx context.Context, userID int64) ([]pages.DeviceStatus, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT label, tier, last_synced_at FROM principals
		 WHERE user_id = $1 ORDER BY label`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]pages.DeviceStatus, 0)
	for rows.Next() {
		var d pages.DeviceStatus
		if err := rows.Scan(&d.Label, &d.Tier, &d.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- books ---

func (a *Admin) booksLeaf(r *http.Request) templ.Component {
	data, err := a.listsData(r, r.URL.Query().Get("sort"))
	if err != nil {
		return pages.ListsPage(pages.ListsData{ErrMsg: err.Error()})
	}
	return pages.ListsPage(data)
}

// booksGrid re-renders just the card grid for a sort change.
func (a *Admin) booksGrid(w http.ResponseWriter, r *http.Request) {
	data, err := a.listsData(r, r.URL.Query().Get("sort"))
	if err != nil {
		data.ErrMsg = err.Error()
	}
	renderTempl(w, r, pages.ListGrid(data))
}

func (a *Admin) listsData(r *http.Request, sortKey string) (pages.ListsData, error) {
	summaries, err := a.bookSummaries(r)
	if err != nil {
		return pages.ListsData{}, err
	}
	sortBookSummaries(summaries, sortKey)
	return pages.ListsData{Summaries: summaries, Sort: normalizeListSort(sortKey)}, nil
}

func (a *Admin) bookSummaries(r *http.Request) ([]store.BookSummary, error) {
	var out []store.BookSummary
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		out, e = s.ListBookSummaries(r.Context(), 6)
		return e
	})
	return out, err
}

func normalizeListSort(s string) string {
	if s == "contacts" {
		return "contacts"
	}
	return "name"
}

func sortBookSummaries(s []store.BookSummary, key string) {
	if normalizeListSort(key) == "contacts" {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].ContactCount != s[j].ContactCount {
				return s[i].ContactCount > s[j].ContactCount
			}
			return s[i].Book.DisplayName < s[j].Book.DisplayName
		})
		return
	}
	sort.SliceStable(s, func(i, j int) bool {
		return s[i].Book.DisplayName < s[j].Book.DisplayName
	})
}

func (a *Admin) bookSlugCheck(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	taken := true
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		taken, e = s.BookSlugTaken(r.Context(), slug)
		return e
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"available": slug != "" && !taken})
}

func (a *Admin) bookAddModal(r *http.Request) (string, templ.Component) {
	return "New list", pages.BookForm(nil)
}

func (a *Admin) bookEditModal(r *http.Request) (string, templ.Component) {
	b, err := a.getBook(r, r.PathValue("id"))
	if err != nil {
		return "Edit list", pages.ErrorBanner(err.Error())
	}
	return "Edit list", pages.BookForm(&b)
}

func (a *Admin) createBook(w http.ResponseWriter, r *http.Request) {
	desc := optional(r.FormValue("description"))
	tiers := r.Form["tiers"]
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		b, err := s.CreateBook(r.Context(), r.FormValue("slug"), r.FormValue("display_name"), desc, 100)
		if err != nil {
			return err
		}
		return s.SetBookTiers(r.Context(), b.ID, tiers)
	})
	a.renderBookList(w, r, err)
}

func (a *Admin) editBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	desc := r.FormValue("description")
	tiers := r.Form["tiers"]
	activeVals := r.Form["active"]
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		b, err := s.GetBook(r.Context(), id)
		if err != nil {
			return err
		}
		if err := s.RenameBook(r.Context(), id, r.FormValue("display_name")); err != nil {
			return err
		}
		if err := s.SetBookDescription(r.Context(), id, &desc); err != nil {
			return err
		}
		if b.IsSystem {
			// The system book's tiers and active flag are fixed.
			return nil
		}
		if err := s.SetBookTiers(r.Context(), id, tiers); err != nil {
			return err
		}
		if len(activeVals) > 0 {
			return s.SetBookActive(r.Context(), id, containsVal(activeVals, "true"))
		}
		return nil
	})
	a.renderBookList(w, r, err)
}

func (a *Admin) deleteBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		return s.DeleteBook(r.Context(), id)
	})
	a.renderBookList(w, r, err)
}

func (a *Admin) renderBookList(w http.ResponseWriter, r *http.Request, cause error) {
	data, err := a.listsData(r, "")
	if err != nil {
		data = pages.ListsData{ErrMsg: err.Error()}
	}
	if cause != nil {
		data.ErrMsg = cause.Error()
	}
	renderTempl(w, r, pages.ListGrid(data), seg.ClearModal())
}

func (a *Admin) listBooks(r *http.Request) ([]store.Book, error) {
	var books []store.Book
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		books, e = s.ListBooks(r.Context())
		return e
	})
	return books, err
}

func (a *Admin) getBook(r *http.Request, raw string) (store.Book, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return store.Book{}, model.ErrNotFound
	}
	var b store.Book
	err = store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		b, e = s.GetBook(r.Context(), id)
		return e
	})
	return b, err
}

// --- devices ---

func (a *Admin) devicesLeaf(r *http.Request) templ.Component {
	views, _, err := a.deviceViews(r)
	if err != nil {
		return pages.DevicesPage(pages.DevicesData{ErrMsg: err.Error()})
	}
	sortKey, dir := deviceQuery(r)
	sortDeviceViews(views, sortKey, dir)
	return pages.DevicesPage(pages.DevicesData{Devices: views, Sort: sortKey, Dir: dir})
}

// devicesRows re-renders just the table body for a sort change.
func (a *Admin) devicesRows(w http.ResponseWriter, r *http.Request) {
	views, _, err := a.deviceViews(r)
	sortKey, dir := deviceQuery(r)
	var data pages.DevicesData
	if err != nil {
		data.ErrMsg = err.Error()
	} else {
		sortDeviceViews(views, sortKey, dir)
		data = pages.DevicesData{Devices: views, Sort: sortKey, Dir: dir}
	}
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"listmeta":{"total":%d,"page":1}}`, len(data.Devices)))
	renderTempl(w, r, pages.DeviceRows(data))
}

func deviceQuery(r *http.Request) (sortKey, dir string) {
	sortKey = r.URL.Query().Get("sort")
	switch sortKey {
	case "tier", "last":
	default:
		sortKey = "label"
	}
	dir = "asc"
	if r.URL.Query().Get("dir") == "desc" {
		dir = "desc"
	}
	return sortKey, dir
}

func sortDeviceViews(views []pages.DeviceView, key, dir string) {
	var less func(i, j int) bool
	switch key {
	case "tier":
		less = func(i, j int) bool { return views[i].Principal.Tier < views[j].Principal.Tier }
	case "last":
		less = func(i, j int) bool {
			return lastSyncBefore(views[i].Principal.LastSyncedAt, views[j].Principal.LastSyncedAt)
		}
	default:
		less = func(i, j int) bool { return views[i].Principal.Label < views[j].Principal.Label }
	}
	if dir == "desc" {
		asc := less
		less = func(i, j int) bool { return asc(j, i) }
	}
	sort.SliceStable(views, less)
}

// lastSyncBefore orders devices by recency; a device that has never synced
// sorts last in ascending order.
func lastSyncBefore(a, b *time.Time) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	return a.Before(*b)
}

func (a *Admin) deviceAddModal(r *http.Request) (string, templ.Component) {
	return "Add device", pages.DeviceAddForm()
}

func (a *Admin) deviceEditModal(r *http.Request) (string, templ.Component) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return "Edit device", pages.ErrorBanner(model.ErrNotFound.Error())
	}
	views, books, err := a.deviceViews(r)
	if err != nil {
		return "Edit device", pages.ErrorBanner(err.Error())
	}
	for _, v := range views {
		if v.Principal.ID == id {
			return "Edit device", pages.DeviceForm(v, activeBooks(books))
		}
	}
	return "Edit device", pages.ErrorBanner(model.ErrNotFound.Error())
}

func (a *Admin) deviceViews(r *http.Request) ([]pages.DeviceView, []store.Book, error) {
	actor := a.actor(r)
	principals, err := store.NewPrincipals(a.pool).ListForUser(r.Context(), actor.UserID)
	if err != nil {
		return nil, nil, err
	}
	var books []store.Book
	overrides := make(map[int64]map[int64]bool, len(principals))
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if books, err = s.ListBooks(r.Context()); err != nil {
			return err
		}
		for _, p := range principals {
			ov, err := s.ListPrincipalOverrides(r.Context(), p.ID)
			if err != nil {
				return err
			}
			overrides[p.ID] = ov
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	views := make([]pages.DeviceView, len(principals))
	for i, p := range principals {
		views[i] = pages.DeviceView{Principal: p, Overrides: overrides[p.ID]}
	}
	return views, books, nil
}

func (a *Admin) createDevice(w http.ResponseWriter, r *http.Request) {
	_, token, err := store.NewPrincipals(a.pool).IssueToken(r.Context(), a.actor(r).UserID,
		r.FormValue("tier"), r.FormValue("label"))
	a.renderDeviceList(w, r, token, err)
}

func (a *Admin) editDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	hidden := make(map[int64]bool)
	for _, v := range r.Form["hide"] {
		if bookID, err := strconv.ParseInt(v, 10, 64); err == nil {
			hidden[bookID] = true
		}
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		if err := s.SetPrincipalLabel(r.Context(), id, r.FormValue("label")); err != nil {
			return err
		}
		if err := s.SetPrincipalTier(r.Context(), id, r.FormValue("tier")); err != nil {
			return err
		}
		return a.reconcileOverrides(r, s, id, hidden)
	})
	a.renderDeviceList(w, r, "", err)
}

// reconcileOverrides writes only the per-book opt-out rows that changed, so a
// no-op save does not bump epochs.
func (a *Admin) reconcileOverrides(r *http.Request, s store.ScopedStore, principalID int64, hidden map[int64]bool) error {
	current, err := s.ListPrincipalOverrides(r.Context(), principalID)
	if err != nil {
		return err
	}
	books, err := s.ListBooks(r.Context())
	if err != nil {
		return err
	}
	for _, b := range books {
		if !b.IsActive {
			continue
		}
		cur, has := current[b.ID]
		want := !hidden[b.ID]
		switch {
		case !has && want, has && cur == want:
		case !has, has && !want:
			if err := s.SetPrincipalOverride(r.Context(), principalID, b.ID, false); err != nil {
				return err
			}
		default:
			if err := s.ClearPrincipalOverride(r.Context(), principalID, b.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Admin) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		return s.DeletePrincipal(r.Context(), id)
	})
	a.renderDeviceList(w, r, "", err)
}

func (a *Admin) renderDeviceList(w http.ResponseWriter, r *http.Request, newToken string, cause error) {
	views, _, err := a.deviceViews(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sortKey, dir := deviceQuery(r)
	sortDeviceViews(views, sortKey, dir)
	data := pages.DevicesData{Devices: views, Sort: sortKey, Dir: dir, ErrMsg: errorText(cause)}
	parts := []templ.Component{pages.DeviceList(data)}
	if newToken != "" {
		parts = append(parts, pages.TokenNoticeOOB(newToken))
	}
	parts = append(parts, seg.ClearModal())
	renderTempl(w, r, parts...)
}

// --- contacts ---

func (a *Admin) contactsLeaf(r *http.Request) templ.Component {
	data, err := a.contactsData(r, contactQueryFrom(r))
	data.ErrMsg = errorText(err)
	return pages.ContactsPage(data)
}

// searchContacts renders only the rows for an htmx search, sort, or page change.
// The HX-Trigger payload lets the Alpine controller refresh the count and pager.
func (a *Admin) searchContacts(w http.ResponseWriter, r *http.Request) {
	data, err := a.contactsData(r, contactQueryFrom(r))
	data.ErrMsg = errorText(err)
	payload, jerr := json.Marshal(map[string]any{
		"listmeta": map[string]any{
			"total": data.Total,
			"page":  data.Page,
			"ids":   pages.ContactIDs(data),
		},
	})
	if jerr != nil {
		payload = []byte(`{"listmeta":{}}`)
	}
	w.Header().Set("HX-Trigger", string(payload))
	renderTempl(w, r, pages.ContactRows(data))
}

func (a *Admin) contactAddModal(r *http.Request) (string, templ.Component) {
	d, err := a.contactFormData(r, 0)
	if err != nil {
		return "Add contact", pages.ErrorBanner(err.Error())
	}
	return "Add contact", pages.ContactForm(d)
}

func (a *Admin) contactEditModal(r *http.Request) (string, templ.Component) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return "Edit contact", pages.ErrorBanner(model.ErrNotFound.Error())
	}
	d, err := a.contactFormData(r, id)
	if err != nil {
		return "Edit contact", pages.ErrorBanner(err.Error())
	}
	title := "Edit contact"
	if d.FullName != "" {
		title += " - " + d.FullName
	}
	return title, pages.ContactForm(d)
}

// contactFormData builds the editor seed. The book picker excludes the default
// book, which is the fallback for untagged contacts.
func (a *Admin) contactFormData(r *http.Request, id int64) (pages.ContactFormData, error) {
	actor := a.actor(r)
	defaultID, err := a.users.DefaultBook(r.Context(), actor.UserID)
	if err != nil {
		return pages.ContactFormData{}, err
	}
	var d pages.ContactFormData
	d.TitleBase = "Add contact"
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		all, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		fallback := resolveFallback(all, defaultID)
		d.Books = choosableBooks(all, fallback.ID)
		d.Selected = []int64{}
		if id == 0 {
			d.Action = "/dashboard/contacts"
			return nil
		}
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		ids, err := s.ContactBookIDs(r.Context(), c.ID)
		if err != nil {
			return err
		}
		for _, b := range ids {
			if b != fallback.ID {
				d.Selected = append(d.Selected, b)
			}
		}
		card, _ := vcard.NewDecoder(bytes.NewBufferString(c.VCardText)).Decode()
		form := contact.Parse(card)
		d.TitleBase = "Edit contact"
		d.ID, d.UID, d.Filename = c.ID, c.UID, c.Filename
		d.FullName = card.Value(vcard.FieldFormattedName)
		if d.FullName == vcardmeta.Unnamed {
			d.FullName = ""
		}
		d.Prefix, d.Given, d.Additional, d.Family, d.Suffix = form.Name.Prefix, form.Name.Given, form.Name.Additional, form.Name.Family, form.Name.Suffix
		d.Fields = form.Fields
		d.Addresses = form.Addresses
		d.Action = "/dashboard/contacts/" + strconv.FormatInt(c.ID, 10) + "/edit"
		return nil
	})
	return d, err
}

// contactQuery is the parsed query string of the contacts list.
type contactQuery struct {
	q            string
	bookID       *int64
	sort         string
	dir          string
	page         int
	sortExplicit bool // the user picked a column, so relevance ranking is off
}

const contactPer = 50

func contactQueryFrom(r *http.Request) contactQuery {
	values := r.URL.Query()
	out := contactQuery{
		q:    values.Get("q"),
		dir:  values.Get("dir"),
		page: atoiDefault(values.Get("page"), 1),
	}
	switch sort := values.Get("sort"); sort {
	case "name", "org", "updated", "created":
		out.sort = sort
		out.sortExplicit = true
	default:
		out.sort = "name"
	}
	if out.dir != "desc" {
		out.dir = "asc"
	}
	if v := values.Get("book"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			out.bookID = &id
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

// contactsData runs the search and assembles everything the page needs. The
// search error is returned separately so the caller decides how to show it.
func (a *Admin) contactsData(r *http.Request, qry contactQuery) (pages.ContactsData, error) {
	views, books, total, err := a.contactViews(r, qry)
	data := pages.ContactsData{
		Contacts:     views,
		Books:        books,
		Q:            qry.q,
		Sort:         qry.sort,
		Dir:          qry.dir,
		SortExplicit: qry.sortExplicit,
		Page:         qry.page,
		Per:          contactPer,
		Total:        total,
	}
	if qry.bookID != nil {
		data.BookID = strconv.FormatInt(*qry.bookID, 10)
		for _, b := range books {
			if b.ID == *qry.bookID {
				data.BookName = b.DisplayName
			}
		}
	}
	return data, err
}

func (a *Admin) contactViews(r *http.Request, qry contactQuery) ([]pages.ContactView, []store.Book, int, error) {
	var views []pages.ContactView
	var active []store.Book
	total := 0
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		books, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		active = activeBooks(books)
		res, err := s.Search(r.Context(), store.SearchParams{
			Query:  qry.q,
			BookID: qry.bookID,
			Page:   qry.page,
			Per:    contactPer,
			Sort:   qry.sort,
			Dir:    qry.dir,
			Rank:   !qry.sortExplicit,
		})
		if err != nil {
			return err
		}
		total = res.Total
		views = make([]pages.ContactView, 0, len(res.Contacts))
		for _, c := range res.Contacts {
			ids, err := s.ContactBookIDs(r.Context(), c.ID)
			if err != nil {
				return err
			}
			tagged := make(map[int64]bool, len(ids))
			for _, id := range ids {
				tagged[id] = true
			}
			views = append(views, contactView(c, tagged))
		}
		return nil
	})
	return views, active, total, err
}

func contactView(c store.Contact, tagged map[int64]bool) pages.ContactView {
	name, tel, email, org := cardFields(c)
	return pages.ContactView{Contact: c, Name: name, Tel: tel, Email: email, Org: org, Books: tagged}
}

func (a *Admin) createContact(w http.ResponseWriter, r *http.Request) {
	form := contactFormFromRequest(r)
	var cause error
	if form.Empty() {
		cause = model.ErrPrecondition
	} else {
		cause = a.putNewContact(r, form)
	}
	a.renderContactList(w, r, cause)
}

func (a *Admin) putNewContact(r *http.Request, form contact.Form) error {
	card, err := contact.Build(vcard.Card{}, form)
	if err != nil {
		return err
	}
	filename, err := newFilename()
	if err != nil {
		return err
	}
	text, uid, meta, err := vcardmeta.Prepare(card, filename)
	if err != nil {
		return err
	}
	desired, err := a.desiredBooks(r)
	if err != nil {
		return err
	}
	return store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		_, err := s.SaveContact(r.Context(), store.PutContactParams{
			Filename: filename, UID: uid, VCardText: text, SearchMeta: meta,
		}, store.Precondition{}, desired)
		return err
	})
}

func (a *Admin) editContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	form := contactFormFromRequest(r)
	desired, err := a.desiredBooks(r)
	if err != nil {
		a.renderContactList(w, r, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		base, err := vcard.NewDecoder(strings.NewReader(c.VCardText)).Decode()
		if err != nil {
			return err
		}
		card, err := contact.Build(base, form)
		if err != nil {
			return err
		}
		text, uid, meta, err := vcardmeta.Prepare(card, c.Filename)
		if err != nil {
			return err
		}
		_, err = s.SaveContact(r.Context(), store.PutContactParams{
			Filename:   c.Filename,
			UID:        uid,
			VCardText:  text,
			SearchMeta: meta,
		}, store.Precondition{}, desired)
		return err
	})
	a.renderContactList(w, r, err)
}

func (a *Admin) deleteContact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		_, err = s.DeleteContact(r.Context(), c.Filename)
		return err
	})
	a.renderContactList(w, r, err)
}

func (a *Admin) contactViewModal(r *http.Request) (string, templ.Component) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return "Contact", pages.ErrorBanner(model.ErrNotFound.Error())
	}
	d, err := a.contactDetailData(r, id)
	if err != nil {
		return "Contact", pages.ErrorBanner(err.Error())
	}
	title := "Contact"
	if d.Name != "" {
		title = d.Name
	}
	return title, pages.ContactDetailPanel(d)
}

// bulkListsModal is the list picker the two bulk list actions open; the selected
// contact ids arrive on the query string and are carried into hidden inputs.
func (a *Admin) bulkListsModal(r *http.Request) (string, templ.Component) {
	mode := r.URL.Query().Get("mode")
	if mode != "set" {
		mode = "add"
	}
	ids := formIDs(r.URL.Query()["id"])
	if len(ids) == 0 {
		return "Lists", pages.ErrorBanner("No contacts selected.")
	}
	var books []store.Book
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		all, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		books = activeBooks(all)
		return nil
	})
	if err != nil {
		return "Lists", pages.ErrorBanner(err.Error())
	}
	title := "Add to lists"
	if mode == "set" {
		title = "Set lists"
	}
	return title, pages.BulkListsModal(mode, ids, books)
}

// bulkContacts applies delete or list tagging to a set of contacts in one tx.
func (a *Admin) bulkContacts(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	ids := formIDs(r.Form["id"])
	listIDs := formIDs(r.Form["list"])
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		switch action {
		case "delete":
			for _, id := range ids {
				c, err := s.LiveContactByID(r.Context(), id)
				if errors.Is(err, model.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if _, err := s.DeleteContact(r.Context(), c.Filename); err != nil {
					return err
				}
			}
		case "add":
			for _, id := range ids {
				if err := s.Retag(r.Context(), id, listIDs, nil); err != nil {
					return err
				}
			}
		case "set":
			for _, id := range ids {
				if err := s.SetContactBooks(r.Context(), id, listIDs); err != nil {
					return err
				}
			}
		default:
			return model.ErrPrecondition
		}
		return nil
	})
	a.renderContactList(w, r, err)
}

// formIDs parses the repeated form/query values that carry contact or list ids.
func formIDs(vals []string) []int64 {
	out := make([]int64, 0, len(vals))
	for _, v := range vals {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// contactDetailData gathers the read-only view: grouped fields, addresses, list
// membership, and timestamps.
func (a *Admin) contactDetailData(r *http.Request, id int64) (pages.ContactDetail, error) {
	var d pages.ContactDetail
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		card, err := vcard.NewDecoder(strings.NewReader(c.VCardText)).Decode()
		if err != nil {
			return err
		}
		form := contact.Parse(card)
		d.ID = c.ID
		d.UID = c.UID
		d.Filename = c.Filename
		d.CreatedAt = c.CreatedAt
		d.UpdatedAt = c.UpdatedAt
		d.Name = card.Value(vcard.FieldFormattedName)
		if d.Name == "" {
			d.Name = c.UID
		}
		d.Groups = detailGroups(form.Fields)
		d.Addresses = form.Addresses
		ids, err := s.ContactBookIDs(r.Context(), c.ID)
		if err != nil {
			return err
		}
		books, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		names := make(map[int64]string, len(books))
		for _, b := range books {
			names[b.ID] = b.DisplayName
		}
		for _, bid := range ids {
			if n, ok := names[bid]; ok {
				d.Lists = append(d.Lists, n)
			}
		}
		sort.Strings(d.Lists)
		return nil
	})
	return d, err
}

// detailGroups folds a contact's property rows into labelled blocks, preserving
// the order the kinds first appear.
func detailGroups(fields []contact.Field) []pages.DetailGroup {
	labels := make(map[string]string)
	for _, k := range contact.Kinds() {
		labels[k.ID] = k.Label
	}
	byKind := make(map[string]*pages.DetailGroup)
	var order []string
	for _, f := range fields {
		g, ok := byKind[f.Kind]
		if !ok {
			label, known := labels[f.Kind]
			if !known {
				label = f.Kind
				if f.Key != "" {
					label = f.Key
				}
			}
			g = &pages.DetailGroup{Label: label}
			byKind[f.Kind] = g
			order = append(order, f.Kind)
		}
		value := f.Value
		if f.Type != "" {
			value = f.Type + ": " + value
		}
		if value != "" {
			g.Values = append(g.Values, value)
		}
	}
	out := make([]pages.DetailGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byKind[k])
	}
	return out
}

func (a *Admin) renderContactList(w http.ResponseWriter, r *http.Request, cause error) {
	data, err := a.contactsData(r, contactQueryFrom(r))
	if cause != nil {
		data.ErrMsg = cause.Error()
	} else {
		data.ErrMsg = errorText(err)
	}
	renderTempl(w, r, pages.ContactsList(data), seg.ClearModal())
}

func (a *Admin) desiredBooks(r *http.Request) ([]int64, error) {
	ids := make([]int64, 0)
	for id := range bookSet(r) {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		fb, err := a.fallbackBookID(r)
		if err != nil {
			return nil, err
		}
		return []int64{fb}, nil
	}
	return ids, nil
}

// fallbackBookID is the default book, or the system book when no default is set.
func (a *Admin) fallbackBookID(r *http.Request) (int64, error) {
	actor := a.actor(r)
	defaultID, err := a.users.DefaultBook(r.Context(), actor.UserID)
	if err != nil {
		return 0, err
	}
	var id int64
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		books, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		id = resolveFallback(books, defaultID).ID
		return nil
	})
	return id, err
}

func resolveFallback(books []store.Book, defaultID *int64) store.Book {
	if defaultID != nil {
		for _, b := range books {
			if b.ID == *defaultID && b.IsActive {
				return b
			}
		}
	}
	for _, b := range books {
		if b.IsSystem {
			return b
		}
	}
	if len(books) > 0 {
		return books[0]
	}
	return store.Book{}
}

func choosableBooks(books []store.Book, fallbackID int64) []store.Book {
	out := make([]store.Book, 0, len(books))
	for _, b := range books {
		if b.IsActive && b.ID != fallbackID {
			out = append(out, b)
		}
	}
	return out
}

// --- settings ---

func (a *Admin) settingsLeaf(r *http.Request) templ.Component {
	s := uictx.From(r.Context())
	books, defaultID, err := a.settingsData(r)
	if err != nil {
		return pages.SettingsPage(s, nil, nil, err.Error())
	}
	return pages.SettingsPage(s, books, defaultID, "")
}

func (a *Admin) settingsModal(r *http.Request) (string, templ.Component) {
	s := uictx.From(r.Context())
	books, defaultID, err := a.settingsData(r)
	msg := errorText(err)
	return "Settings", pages.SettingsModal(s, books, defaultID, msg)
}

func (a *Admin) settingsData(r *http.Request) ([]store.Book, *int64, error) {
	books, err := a.listBooks(r)
	if err != nil {
		return nil, nil, err
	}
	def, err := a.users.DefaultBook(r.Context(), a.actor(r).UserID)
	return books, def, err
}

func (a *Admin) saveSettings(w http.ResponseWriter, r *http.Request) {
	target := r.FormValue("target")
	if target == "" {
		target = "#settings-panel"
	}
	var cause error
	if raw := r.FormValue("default_book_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			cause = model.ErrPrecondition
		} else {
			cause = a.users.SetDefaultBook(r.Context(), a.actor(r).UserID, id)
		}
	}
	books, def, err := a.settingsData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.SettingsBookForm(books, def, errorText(cause), target))
}

// --- helpers ---

// cardFields reads the display fields the edit form prefills; the name falls
// back to the UID.
func cardFields(c store.Contact) (name, tel, email, org string) {
	card, err := vcard.NewDecoder(bytes.NewBufferString(c.VCardText)).Decode()
	if err != nil {
		return c.UID, "", "", ""
	}
	name = card.Value(vcard.FieldFormattedName)
	if name == "" {
		name = c.UID
	}
	return name, card.Value(vcard.FieldTelephone), card.Value(vcard.FieldEmail), card.Value(vcard.FieldOrganization)
}

// contactFormFromRequest reads the structured editor form.
func contactFormFromRequest(r *http.Request) contact.Form {
	return contact.Form{
		Name: contact.Name{
			Prefix:     strings.TrimSpace(r.FormValue("n_prefix")),
			Given:      strings.TrimSpace(r.FormValue("n_given")),
			Additional: strings.TrimSpace(r.FormValue("n_additional")),
			Family:     strings.TrimSpace(r.FormValue("n_family")),
			Suffix:     strings.TrimSpace(r.FormValue("n_suffix")),
		},
		Fields:    contactFieldsFromForm(r),
		Addresses: contactAddressesFromForm(r),
	}
}

func contactFieldsFromForm(r *http.Request) []contact.Field {
	kinds := r.Form["row_kind"]
	keys := r.Form["row_key"]
	types := r.Form["row_type"]
	vals := r.Form["row_value"]
	rows := make([]contact.Field, 0, len(kinds))
	for i := range kinds {
		rows = append(rows, contact.Field{
			Kind: at(kinds, i), Key: at(keys, i), Type: at(types, i), Value: at(vals, i),
		})
	}
	return rows
}

func contactAddressesFromForm(r *http.Request) []contact.Address {
	types := r.Form["adr_type"]
	streets := r.Form["adr_street"]
	cities := r.Form["adr_city"]
	poboxes := r.Form["adr_pobox"]
	exts := r.Form["adr_ext"]
	regions := r.Form["adr_region"]
	postals := r.Form["adr_postal"]
	countries := r.Form["adr_country"]
	rows := make([]contact.Address, 0, len(types))
	for i := range types {
		rows = append(rows, contact.Address{
			Type:    at(types, i),
			Street:  at(streets, i),
			City:    at(cities, i),
			Pobox:   at(poboxes, i),
			Ext:     at(exts, i),
			Region:  at(regions, i),
			Postal:  at(postals, i),
			Country: at(countries, i),
		})
	}
	return rows
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

func bookSet(r *http.Request) map[int64]bool {
	want := make(map[int64]bool)
	for _, v := range r.Form["book"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			want[id] = true
		}
	}
	return want
}

func newFilename() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b) + ".vcf", nil
}

func (a *Admin) actor(r *http.Request) model.Actor {
	s := uictx.From(r.Context())
	return model.Actor{UserID: s.UserID, Username: s.Username}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func renderTempl(w http.ResponseWriter, r *http.Request, comps ...templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templ.Join(comps...).Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func activeBooks(books []store.Book) []store.Book {
	out := make([]store.Book, 0, len(books))
	for _, b := range books {
		if b.IsActive {
			out = append(out, b)
		}
	}
	return out
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func containsVal(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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

// mutate guarantees a session and a matching CSRF token before the handler
// runs, for cookie-authenticated state changes.
func (a *Admin) mutate(next http.Handler) http.Handler {
	return a.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
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
