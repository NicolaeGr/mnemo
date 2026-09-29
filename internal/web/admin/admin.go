// Package admin serves the htmx admin UI: session-cookie login, then a
// dashboard behind the seg segment stack. It talks to the store directly, the
// same way the REST handlers do, so the UI never round-trips over HTTP to
// itself.
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5/pgxpool"

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

	mux.Handle("GET /dashboard", a.dashPage("Overview", a.overviewLeaf))
	mux.Handle("GET /dashboard/books", a.dashPage("Books", a.booksLeaf))
	mux.Handle("POST /dashboard/books", a.mutate(http.HandlerFunc(a.createBook)))
	mux.Handle("POST /dashboard/books/{id}/rename", a.mutate(http.HandlerFunc(a.renameBook)))
	mux.Handle("POST /dashboard/books/{id}/active", a.mutate(http.HandlerFunc(a.setBookActive)))
	mux.Handle("POST /dashboard/books/{id}/tiers", a.mutate(http.HandlerFunc(a.setBookTiers)))
	mux.Handle("POST /dashboard/books/{id}/delete", a.mutate(http.HandlerFunc(a.deleteBook)))
	mux.Handle("GET /dashboard/devices", a.dashPage("Devices", a.devicesLeaf))
	mux.Handle("POST /dashboard/devices", a.mutate(http.HandlerFunc(a.createDevice)))
	mux.Handle("POST /dashboard/devices/{id}/rename", a.mutate(http.HandlerFunc(a.renameDevice)))
	mux.Handle("POST /dashboard/devices/{id}/tier", a.mutate(http.HandlerFunc(a.setDeviceTier)))
	mux.Handle("POST /dashboard/devices/{id}/overrides", a.mutate(http.HandlerFunc(a.setDeviceOverrides)))
	mux.Handle("POST /dashboard/devices/{id}/delete", a.mutate(http.HandlerFunc(a.deleteDevice)))
	mux.Handle("GET /dashboard/contacts", a.dashPage("Contacts", a.contactsLeaf))
	mux.Handle("GET /dashboard/contacts/search", a.requireSession(http.HandlerFunc(a.searchContacts)))
	mux.Handle("POST /dashboard/contacts", a.mutate(http.HandlerFunc(a.createContact)))
	mux.Handle("POST /dashboard/contacts/{id}/tags", a.mutate(http.HandlerFunc(a.setContactTags)))
	mux.Handle("POST /dashboard/contacts/{id}/delete", a.mutate(http.HandlerFunc(a.deleteContact)))
	mux.Handle("GET /dashboard/settings", a.dashPage("Settings", a.settingsLeaf))
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
	s := uictx.From(r.Context())
	books, defaultID, err := a.settingsData(r)
	if err != nil {
		return pages.SettingsPage(s, nil, nil, err.Error())
	}
	return pages.SettingsPage(s, books, defaultID, "")
}

func (a *Admin) settingsData(r *http.Request) ([]store.Book, *int64, error) {
	var books []store.Book
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		var e error
		books, e = s.ListBooks(r.Context())
		return e
	})
	if err != nil {
		return nil, nil, err
	}
	def, err := a.users.DefaultBook(r.Context(), a.actor(r).UserID)
	return books, def, err
}

func (a *Admin) saveSettings(w http.ResponseWriter, r *http.Request) {
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
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	renderTempl(w, r, pages.SettingsPanel(books, def, msg))
}

func (a *Admin) booksLeaf(r *http.Request) templ.Component {
	books, err := a.listBooks(r)
	if err != nil {
		return pages.BookList(nil, err.Error())
	}
	return pages.BookList(books, "")
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

func (a *Admin) createBook(w http.ResponseWriter, r *http.Request) {
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		_, err := s.CreateBook(r.Context(), r.FormValue("slug"), r.FormValue("display_name"), nil, 100)
		return err
	})
	a.renderBookList(w, r, err)
}

func (a *Admin) renameBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	desc := r.FormValue("description")
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		if err := s.RenameBook(r.Context(), id, r.FormValue("display_name")); err != nil {
			return err
		}
		return s.SetBookDescription(r.Context(), id, &desc)
	})
	a.renderBookList(w, r, err)
}

func (a *Admin) setBookActive(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	active := r.FormValue("active") == "true"
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		return s.SetBookActive(r.Context(), id, active)
	})
	a.renderBookList(w, r, err)
}

func (a *Admin) setBookTiers(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tiers := r.Form["tiers"]
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		return s.SetBookTiers(r.Context(), id, tiers)
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

// renderBookList re-renders the list fragment after a mutation; the caller's
// htmx attributes target #books-list, so errors surface in the same place.
func (a *Admin) renderBookList(w http.ResponseWriter, r *http.Request, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	books, err := a.listBooks(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.BookList(books, msg))
}

func (a *Admin) actor(r *http.Request) model.Actor {
	s := uictx.From(r.Context())
	return model.Actor{UserID: s.UserID, Username: s.Username}
}

func (a *Admin) contactsLeaf(r *http.Request) templ.Component {
	q := r.URL.Query().Get("q")
	views, books, err := a.contactViews(r, q)
	if err != nil {
		return pages.ContactsPage(nil, nil, q, err.Error())
	}
	return pages.ContactsPage(views, books, q, "")
}

func (a *Admin) searchContacts(w http.ResponseWriter, r *http.Request) {
	a.renderContactList(w, r, "", nil)
}

func (a *Admin) contactViews(r *http.Request, q string) ([]pages.ContactView, []store.Book, error) {
	var views []pages.ContactView
	var active []store.Book
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		books, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		for _, b := range books {
			if b.IsActive {
				active = append(active, b)
			}
		}
		res, err := s.Search(r.Context(), store.SearchParams{Query: q, Per: 50})
		if err != nil {
			return err
		}
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
			views = append(views, pages.ContactView{Contact: c, Name: displayName(c), Books: tagged})
		}
		return nil
	})
	return views, active, err
}

// displayName reads FN from the stored vcard, falling back to the UID.
func displayName(c store.Contact) string {
	if card, err := vcard.NewDecoder(bytes.NewBufferString(c.VCardText)).Decode(); err == nil {
		if fn := card.Value(vcard.FieldFormattedName); fn != "" {
			return fn
		}
	}
	return c.UID
}

func (a *Admin) setContactTags(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	want := make(map[int64]bool)
	for _, v := range r.Form["book"] {
		if bookID, err := strconv.ParseInt(v, 10, 64); err == nil {
			want[bookID] = true
		}
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		ids, err := s.ContactBookIDs(r.Context(), id)
		if err != nil {
			return err
		}
		current := make(map[int64]bool, len(ids))
		for _, b := range ids {
			current[b] = true
		}
		var add, remove []int64
		for b := range want {
			if !current[b] {
				add = append(add, b)
			}
		}
		for b := range current {
			if !want[b] {
				remove = append(remove, b)
			}
		}
		return s.Retag(r.Context(), id, add, remove)
	})
	a.renderContactList(w, r, "", err)
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
	a.renderContactList(w, r, "", err)
}

func (a *Admin) renderContactList(w http.ResponseWriter, r *http.Request, _ string, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	views, books, err := a.contactViews(r, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.ContactList(views, books, msg))
}

// createContact builds a card from the form fields and stores it under a
// server-generated filename, into the system book by default.
func (a *Admin) createContact(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	tel := strings.TrimSpace(r.FormValue("tel"))
	email := strings.TrimSpace(r.FormValue("email"))
	var cause error
	if name == "" {
		cause = model.ErrPrecondition
	} else {
		cause = a.storeNewContact(r, name, tel, email)
	}
	a.renderContactList(w, r, "", cause)
}

func (a *Admin) storeNewContact(r *http.Request, name, tel, email string) error {
	card := vcard.Card{}
	card.SetValue(vcard.FieldVersion, "3.0")
	card.SetValue(vcard.FieldFormattedName, name)
	if tel != "" {
		card.SetValue(vcard.FieldTelephone, tel)
	}
	if email != "" {
		card.SetValue(vcard.FieldEmail, email)
	}
	vcardmeta.EnsureFormattedName(card)
	text := vcardmeta.CanonicalText(card)
	meta, err := json.Marshal(vcardmeta.SearchMeta(card))
	if err != nil {
		return err
	}
	filename := newFilename()
	uid := vcardmeta.DeriveUID(card, filename)
	return store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		_, err := s.PutContact(r.Context(), store.PutContactParams{
			Filename: filename, UID: uid, VCardText: text, SearchMeta: meta,
		}, store.Precondition{})
		return err
	})
}

func newFilename() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b) + ".vcf"
}

func (a *Admin) devicesLeaf(r *http.Request) templ.Component {
	views, books, err := a.deviceViews(r)
	if err != nil {
		return pages.DeviceList(nil, nil, "", err.Error())
	}
	return pages.DeviceList(views, books, "", "")
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

func (a *Admin) renameDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		return s.SetPrincipalLabel(r.Context(), id, r.FormValue("label"))
	})
	a.renderDeviceList(w, r, "", err)
}

func (a *Admin) setDeviceTier(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		return s.SetPrincipalTier(r.Context(), id, r.FormValue("tier"))
	})
	a.renderDeviceList(w, r, "", err)
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

// setDeviceOverrides reconciles a device's per-book opt-outs from the "hide"
// checkboxes, only writing rows that actually change so epochs don't bump on a
// no-op save.
func (a *Admin) setDeviceOverrides(w http.ResponseWriter, r *http.Request) {
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
		current, err := s.ListPrincipalOverrides(r.Context(), id)
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
				// Nothing to change.
			case !has, has && !want:
				if err := s.SetPrincipalOverride(r.Context(), id, b.ID, false); err != nil {
					return err
				}
			default:
				if err := s.ClearPrincipalOverride(r.Context(), id, b.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	a.renderDeviceList(w, r, "", err)
}

func (a *Admin) renderDeviceList(w http.ResponseWriter, r *http.Request, newToken string, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	views, books, err := a.deviceViews(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.DeviceList(views, books, newToken, msg))
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

func renderTempl(w http.ResponseWriter, r *http.Request, comp templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := comp.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
