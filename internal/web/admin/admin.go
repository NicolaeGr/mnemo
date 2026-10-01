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
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/emersion/go-vcard"
	"github.com/jackc/pgx/v5/pgxpool"

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

	mux.Handle("GET /dashboard/books", a.dashPage("Books", a.booksLeaf))
	mux.Handle("GET /dashboard/books/slug", a.requireSession(http.HandlerFunc(a.bookSlugCheck)))
	mux.Handle("GET /dashboard/books/new", a.modal(a.bookAddModal))
	mux.Handle("GET /dashboard/books/{id}/edit", a.modal(a.bookEditModal))
	mux.Handle("POST /dashboard/books", a.mutate(http.HandlerFunc(a.createBook)))
	mux.Handle("POST /dashboard/books/{id}/edit", a.mutate(http.HandlerFunc(a.editBook)))
	mux.Handle("POST /dashboard/books/{id}/delete", a.mutate(http.HandlerFunc(a.deleteBook)))

	mux.Handle("GET /dashboard/devices", a.dashPage("Devices", a.devicesLeaf))
	mux.Handle("GET /dashboard/devices/new", a.modal(a.deviceAddModal))
	mux.Handle("GET /dashboard/devices/{id}/edit", a.modal(a.deviceEditModal))
	mux.Handle("POST /dashboard/devices", a.mutate(http.HandlerFunc(a.createDevice)))
	mux.Handle("POST /dashboard/devices/{id}/edit", a.mutate(http.HandlerFunc(a.editDevice)))
	mux.Handle("POST /dashboard/devices/{id}/delete", a.mutate(http.HandlerFunc(a.deleteDevice)))

	mux.Handle("GET /dashboard/contacts", a.dashPage("Contacts", a.contactsLeaf))
	mux.Handle("GET /dashboard/contacts/search", a.requireSession(http.HandlerFunc(a.searchContacts)))
	mux.Handle("GET /dashboard/contacts/new", a.modal(a.contactAddModal))
	mux.Handle("GET /dashboard/contacts/{id}/edit", a.modal(a.contactEditModal))
	mux.Handle("POST /dashboard/contacts", a.mutate(http.HandlerFunc(a.createContact)))
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
		seg.PageWith(w, r, title, leaf(r), layouts.SidebarNavOOB())
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

// --- books ---

func (a *Admin) booksLeaf(r *http.Request) templ.Component {
	books, err := a.listBooks(r)
	if err != nil {
		return pages.BooksPage(nil, err.Error())
	}
	return pages.BooksPage(books, "")
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
	return "Add book", pages.BookForm(nil)
}

func (a *Admin) bookEditModal(r *http.Request) (string, templ.Component) {
	b, err := a.getBook(r, r.PathValue("id"))
	if err != nil {
		return "Edit book", pages.ErrorBanner(err.Error())
	}
	return "Edit book", pages.BookForm(&b)
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
	msg := errorText(cause)
	books, err := a.listBooks(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.BookList(books, msg), seg.ClearModal())
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
		return pages.DevicesPage(nil, err.Error())
	}
	return pages.DevicesPage(views, "")
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
	msg := errorText(cause)
	views, _, err := a.deviceViews(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	parts := []templ.Component{pages.DeviceList(views, msg)}
	if newToken != "" {
		parts = append(parts, pages.TokenNoticeOOB(newToken))
	}
	parts = append(parts, seg.ClearModal())
	renderTempl(w, r, parts...)
}

// --- contacts ---

func (a *Admin) contactsLeaf(r *http.Request) templ.Component {
	q := r.URL.Query().Get("q")
	views, books, err := a.contactViews(r, q)
	if err != nil {
		return pages.ContactsPage(nil, nil, q, err.Error())
	}
	return pages.ContactsPage(views, books, q, "")
}

func (a *Admin) searchContacts(w http.ResponseWriter, r *http.Request) {
	views, books, err := a.contactViews(r, r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.ContactList(views, books, ""))
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

func (a *Admin) contactViews(r *http.Request, q string) ([]pages.ContactView, []store.Book, error) {
	var views []pages.ContactView
	var active []store.Book
	err := store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		books, err := s.ListBooks(r.Context())
		if err != nil {
			return err
		}
		active = activeBooks(books)
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
			views = append(views, contactView(c, tagged))
		}
		return nil
	})
	return views, active, err
}

func contactView(c store.Contact, tagged map[int64]bool) pages.ContactView {
	name, tel, email := cardFields(c)
	return pages.ContactView{Contact: c, Name: name, Tel: tel, Email: email, Books: tagged}
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
	meta, err := json.Marshal(vcardmeta.SearchMeta(card))
	if err != nil {
		return err
	}
	filename := newFilename()
	uid := vcardmeta.DeriveUID(card, filename)
	desired, err := a.desiredBooks(r)
	if err != nil {
		return err
	}
	return store.WithTx(r.Context(), a.pool, a.actor(r), func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, a.actor(r).UserID); err != nil {
			return err
		}
		if _, err := s.PutContact(r.Context(), store.PutContactParams{
			Filename: filename, UID: uid, VCardText: vcardmeta.CanonicalText(card), SearchMeta: meta,
		}, store.Precondition{}); err != nil {
			return err
		}
		c, err := s.LiveContactByFilename(r.Context(), filename)
		if err != nil {
			return err
		}
		return s.SetContactBooks(r.Context(), c.ID, desired)
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
		meta, err := json.Marshal(vcardmeta.SearchMeta(card))
		if err != nil {
			return err
		}
		if _, err := s.PutContact(r.Context(), store.PutContactParams{
			Filename:   c.Filename,
			UID:        vcardmeta.DeriveUID(card, c.Filename),
			VCardText:  vcardmeta.CanonicalText(card),
			SearchMeta: meta,
		}, store.Precondition{}); err != nil {
			return err
		}
		return s.SetContactBooks(r.Context(), id, desired)
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

func (a *Admin) renderContactList(w http.ResponseWriter, r *http.Request, cause error) {
	msg := errorText(cause)
	views, books, err := a.contactViews(r, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTempl(w, r, pages.ContactList(views, books, msg), seg.ClearModal())
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
func cardFields(c store.Contact) (name, tel, email string) {
	card, err := vcard.NewDecoder(bytes.NewBufferString(c.VCardText)).Decode()
	if err != nil {
		return c.UID, "", ""
	}
	name = card.Value(vcard.FieldFormattedName)
	if name == "" {
		name = c.UID
	}
	return name, card.Value(vcard.FieldTelephone), card.Value(vcard.FieldEmail)
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

func newFilename() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b) + ".vcf"
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
