// Package api exposes the REST surface that drives book and tag mutations and
// account management, so device principals actually see epoch bumps. Auth is
// the same Bearer-or-Basic scheme as the DAV plane except for user signup,
// which is anonymous. Every other request acts as the resolved user.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/nicolaegr/mnemo/internal/auth"
	"github.com/nicolaegr/mnemo/internal/contact"
	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

type api struct {
	users *store.Users
	pool  *pgxpool.Pool
}

// New builds the /api/v1 handler.
func New(users *store.Users, pool *pgxpool.Pool) http.Handler {
	a := &api{users: users, pool: pool}
	r := chi.NewRouter()

	r.Route("/principals", func(r chi.Router) {
		r.Get("/", a.listPrincipals)
		r.Post("/", a.createPrincipal)
		r.Patch("/{id}", a.updatePrincipal)
		r.Delete("/{id}", a.deletePrincipal)
		r.Put("/{id}/overrides/{book}", a.setOverride)
		r.Delete("/{id}/overrides/{book}", a.clearOverride)
	})

	r.Route("/books", func(r chi.Router) {
		r.Get("/", a.listBooks)
		r.Post("/", a.createBook)
		r.Post("/reorder", a.reorderBooks)
		r.Patch("/{id}", a.updateBook)
		r.Delete("/{id}", a.deleteBook)
	})

	r.Route("/contacts", func(r chi.Router) {
		r.Get("/", a.searchContacts)
		r.Post("/", a.createContact)
		r.Get("/{id}", a.getContact)
		r.Patch("/{id}", a.updateContact)
		r.Delete("/{id}", a.deleteContact)
		r.Post("/{id}/tags", a.tagContact)
	})

	r.Route("/me", func(r chi.Router) {
		r.Get("/", a.me)
		r.Patch("/settings", a.updateSettings)
	})

	r.Post("/events/force-resync", a.forceResync)

	authed := auth.RequireDAV(users, store.NewPrincipals(pool), r)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost && req.URL.Path == "/users" {
			a.signup(w, req)
			return
		}
		authed.ServeHTTP(w, req)
	})
}

type bookBody struct {
	Slug        string  `json:"slug"`
	DisplayName string  `json:"display_name"`
	Description *string `json:"description"`
	SortOrder   *int    `json:"sort_order"`
}

type bookOut struct {
	ID          int64    `json:"id"`
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	Description *string  `json:"description"`
	IsActive    bool     `json:"is_active"`
	SyncedTiers []string `json:"synced_tiers"`
}

func (a *api) listBooks(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var out []bookOut
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		books, err := s.ActiveBooks(r.Context())
		if err != nil {
			return err
		}
		out = make([]bookOut, 0, len(books))
		for _, b := range books {
			out = append(out, bookOut{b.ID, b.Slug, b.DisplayName, b.Description, b.IsActive, b.SyncedTiers})
		}
		return nil
	})
	writeJSON(w, 200, out, err)
}

func (a *api) createBook(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body bookBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	sortOrder := 100
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	var book store.Book
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		book, err = s.CreateBook(r.Context(), body.Slug, body.DisplayName, body.Description, sortOrder)
		return err
	})
	writeJSON(w, 201, bookOut{book.ID, book.Slug, book.DisplayName, book.Description, book.IsActive, book.SyncedTiers}, err)
}

type bookPatch struct {
	Active      *bool    `json:"active"`
	SyncedTiers []string `json:"synced_tiers"`
	DisplayName *string  `json:"display_name"`
	Description *string  `json:"description"`
}

func (a *api) updateBook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body bookPatch
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		if body.Active != nil {
			if err := s.SetBookActive(r.Context(), id, *body.Active); err != nil {
				return err
			}
		}
		if body.SyncedTiers != nil {
			if err := s.SetBookTiers(r.Context(), id, body.SyncedTiers); err != nil {
				return err
			}
		}
		if body.DisplayName != nil {
			if err := s.RenameBook(r.Context(), id, *body.DisplayName); err != nil {
				return err
			}
		}
		if body.Description != nil {
			if err := s.SetBookDescription(r.Context(), id, body.Description); err != nil {
				return err
			}
		}
		return nil
	})
	writeErr(w, err)
}

// reorderBooks applies a priority order (most important first) to the caller's
// books by setting sort_order from position.
func (a *api) reorderBooks(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var ids []int64
	if err := decodeJSON(r, &ids); err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		return s.ReorderBooks(r.Context(), ids)
	})
	writeErr(w, err)
}

func (a *api) deleteBook(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		return s.DeleteBook(r.Context(), id)
	})
	writeErr(w, err)
}

type fullContactOut struct {
	ID        int64             `json:"id"`
	UID       string            `json:"uid"`
	Filename  string            `json:"filename"`
	ETag      string            `json:"etag"`
	VCardText string            `json:"vcard_text"`
	Name      contact.Name      `json:"name"`
	Fields    []contact.Field   `json:"fields"`
	Addresses []contact.Address `json:"addresses"`
}

// fullContact projects a stored card into both shapes: the raw vcard and the
// structured form the admin UI edits.
func fullContact(c store.Contact) fullContactOut {
	card, _ := vcard.NewDecoder(strings.NewReader(c.VCardText)).Decode()
	form := contact.Parse(card)
	return fullContactOut{
		ID: c.ID, UID: c.UID, Filename: c.Filename, ETag: c.ETag, VCardText: c.VCardText,
		Name: form.Name, Fields: form.Fields, Addresses: form.Addresses,
	}
}

// getContact returns one live contact including its raw vcard.
func (a *api) getContact(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var out fullContactOut
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		out = fullContact(c)
		return nil
	})
	writeJSON(w, 200, out, err)
}

type contactIn struct {
	VCard     string            `json:"vcard"`
	Name      contact.Name      `json:"name"`
	Fields    []contact.Field   `json:"fields"`
	Addresses []contact.Address `json:"addresses"`
	Tags      []int64           `json:"tags"`
}

func (in contactIn) form() contact.Form {
	return contact.Form{Name: in.Name, Fields: in.Fields, Addresses: in.Addresses}
}

// cardFromIn renders the body into a card: a raw vcard when given, otherwise the
// structured form built over base. A body with neither is a precondition failure.
func cardFromIn(base vcard.Card, in contactIn) (vcard.Card, error) {
	if strings.TrimSpace(in.VCard) != "" {
		card, err := parseVCard(in.VCard)
		if err != nil {
			return nil, model.ErrPrecondition
		}
		return card, nil
	}
	if in.form().Empty() {
		return nil, model.ErrPrecondition
	}
	return contact.Build(base, in.form())
}

// createContact stores a new card under a server-generated filename, tagging into
// the system book unless tags are given.
func (a *api) createContact(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body contactIn
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	card, err := cardFromIn(vcard.Card{}, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	vcardmeta.EnsureFormattedName(card)
	text := vcardmeta.CanonicalText(card)
	if text == "" {
		writeErr(w, model.ErrPrecondition)
		return
	}
	meta, err := json.Marshal(vcardmeta.SearchMeta(card))
	if err != nil {
		writeErr(w, err)
		return
	}
	filename := uuidFilename()
	uid := vcardmeta.DeriveUID(card, filename)

	var out fullContactOut
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		if _, err := s.PutContact(r.Context(), store.PutContactParams{
			Filename: filename, UID: uid, VCardText: text, SearchMeta: meta,
		}, store.Precondition{}); err != nil {
			return err
		}
		c, err := s.LiveContactByFilename(r.Context(), filename)
		if err != nil {
			return err
		}
		if body.Tags != nil {
			if err := s.SetContactBooks(r.Context(), c.ID, body.Tags); err != nil {
				return err
			}
		}
		out = fullContact(c)
		return nil
	})
	writeJSON(w, 201, out, err)
}

// updateContact replaces an existing contact's editable fields, keeping its
// identity and binary properties (photo, logo, uid) from the stored card.
func (a *api) updateContact(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body contactIn
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}

	var out fullContactOut
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		base, _ := vcard.NewDecoder(strings.NewReader(c.VCardText)).Decode()
		card, err := cardFromIn(base, body)
		if err != nil {
			return err
		}
		vcardmeta.EnsureFormattedName(card)
		text := vcardmeta.CanonicalText(card)
		if text == "" {
			return model.ErrPrecondition
		}
		meta, err := json.Marshal(vcardmeta.SearchMeta(card))
		if err != nil {
			return err
		}
		uid := vcardmeta.DeriveUID(card, c.Filename)
		if _, err := s.PutContact(r.Context(), store.PutContactParams{
			Filename: c.Filename, UID: uid, VCardText: text, SearchMeta: meta,
		}, store.Precondition{}); err != nil {
			return err
		}
		updated, err := s.LiveContactByFilename(r.Context(), c.Filename)
		if err != nil {
			return err
		}
		if body.Tags != nil {
			if err := s.SetContactBooks(r.Context(), updated.ID, body.Tags); err != nil {
				return err
			}
		}
		out = fullContact(updated)
		return nil
	})
	writeJSON(w, 200, out, err)
}

func parseVCard(text string) (vcard.Card, error) {
	return vcard.NewDecoder(strings.NewReader(text)).Decode()
}

func uuidFilename() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b) + ".vcf"
}

// deleteContact soft-deletes one live contact by id.
func (a *api) deleteContact(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		c, err := s.LiveContactByID(r.Context(), id)
		if err != nil {
			return err
		}
		_, err = s.DeleteContact(r.Context(), c.Filename)
		return err
	})
	writeErr(w, err)
}

type tagBody struct {
	Add    []int64 `json:"add"`
	Remove []int64 `json:"remove"`
}

func (a *api) tagContact(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body tagBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		return s.Retag(r.Context(), id, body.Add, body.Remove)
	})
	writeErr(w, err)
}

type contactOut struct {
	ID       int64  `json:"id"`
	UID      string `json:"uid"`
	Filename string `json:"filename"`
	ETag     string `json:"etag"`
}

func (a *api) searchContacts(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	q := r.URL.Query().Get("q")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	var bookID *int64
	if raw := r.URL.Query().Get("book"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeErr(w, model.ErrPrecondition)
			return
		}
		bookID = &id
	}
	var result struct {
		Contacts []contactOut `json:"contacts"`
		NextPage bool         `json:"next_page"`
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		res, err := s.Search(r.Context(), store.SearchParams{Query: q, BookID: bookID, Page: page})
		if err != nil {
			return err
		}
		result.Contacts = make([]contactOut, 0, len(res.Contacts))
		for _, c := range res.Contacts {
			result.Contacts = append(result.Contacts, contactOut{c.ID, c.UID, c.Filename, c.ETag})
		}
		result.NextPage = res.NextPage
		return nil
	})
	writeJSON(w, 200, result, err)
}

type principalIn struct {
	Tier  string `json:"tier"`
	Label string `json:"label"`
}

type principalOut struct {
	ID    int64  `json:"id"`
	Tier  string `json:"tier"`
	Label string `json:"label"`
	Token string `json:"token,omitempty"`
}

func (a *api) createPrincipal(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body principalIn
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	p, token, err := store.NewPrincipals(a.pool).IssueToken(r.Context(), actor.UserID, body.Tier, body.Label)
	writeJSON(w, 201, principalOut{p.ID, p.Tier, p.Label, token}, err)
}

func (a *api) listPrincipals(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	list, err := store.NewPrincipals(a.pool).ListForUser(r.Context(), actor.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]principalOut, 0, len(list))
	for _, p := range list {
		out = append(out, principalOut{p.ID, p.Tier, p.Label, ""})
	}
	writeJSON(w, 200, out, nil)
}

type principalPatch struct {
	Tier  *string `json:"tier"`
	Label *string `json:"label"`
}

// updatePrincipal edits a device's tier and/or label. A tier change flips what
// a client syncs, so it takes the sync lock and bumps the principal's epoch; a
// label is device-invisible and skips both.
func (a *api) updatePrincipal(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body principalPatch
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.Tier == nil && body.Label == nil {
		writeErr(w, model.ErrPrecondition)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if body.Tier != nil {
			if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
				return err
			}
		}
		if body.Label != nil {
			if err := s.SetPrincipalLabel(r.Context(), id, *body.Label); err != nil {
				return err
			}
		}
		if body.Tier != nil {
			if err := s.SetPrincipalTier(r.Context(), id, *body.Tier); err != nil {
				return err
			}
		}
		return nil
	})
	writeErr(w, err)
}

// deletePrincipal revokes a device; its token stops resolving and its overrides
// cascade. No epoch bump, so no lock.
func (a *api) deletePrincipal(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		return s.DeletePrincipal(r.Context(), id)
	})
	writeErr(w, err)
}

type overrideBody struct {
	Enabled bool `json:"enabled"`
}

// setOverride upserts whether a principal may see a book.
func (a *api) setOverride(w http.ResponseWriter, r *http.Request) {
	pid, err := idParam(w, r)
	if err != nil {
		return
	}
	book, err := bookParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body overrideBody
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		return s.SetPrincipalOverride(r.Context(), pid, book, body.Enabled)
	})
	writeErr(w, err)
}

// clearOverride removes a principal's opt-out for a book.
func (a *api) clearOverride(w http.ResponseWriter, r *http.Request) {
	pid, err := idParam(w, r)
	if err != nil {
		return
	}
	book, err := bookParam(w, r)
	if err != nil {
		return
	}
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		if err := store.LockSync(r.Context(), s.Tx, actor.UserID); err != nil {
			return err
		}
		return s.ClearPrincipalOverride(r.Context(), pid, book)
	})
	writeErr(w, err)
}

type meOut struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// me returns the authenticated account.
func (a *api) me(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	u, err := a.users.ByID(r.Context(), actor.UserID)
	writeJSON(w, 200, meOut{u.ID, u.Username, u.Email, u.Name}, err)
}

type settingsPatch struct {
	DefaultBookID *int64 `json:"default_book_id"`
}

// updateSettings points the account's default book at an owned book.
func (a *api) updateSettings(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body settingsPatch
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if body.DefaultBookID == nil {
		writeErr(w, model.ErrPrecondition)
		return
	}
	err = a.users.SetDefaultBook(r.Context(), actor.UserID, *body.DefaultBookID)
	writeErr(w, err)
}

type signupIn struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// usernameShape mirrors the users.username check constraint, so a bad shape is
// rejected as 400 rather than surfacing as a 500 from the constraint.
var usernameShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

func validateSignup(body signupIn) error {
	if !usernameShape.MatchString(body.Username) {
		return model.ErrPrecondition
	}
	if !strings.Contains(body.Email, "@") || strings.ContainsAny(body.Email, " \t") {
		return model.ErrPrecondition
	}
	if body.Password == "" {
		return model.ErrPrecondition
	}
	return nil
}

// signup creates an account with its system book. Anonymous.
func (a *api) signup(w http.ResponseWriter, r *http.Request) {
	var body signupIn
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, err)
		return
	}
	if err := validateSignup(body); err != nil {
		writeErr(w, err)
		return
	}
	u, _, err := a.users.Signup(r.Context(), body.Username, body.Email, body.Name, body.Password, bcrypt.DefaultCost)
	writeJSON(w, 201, meOut{u.ID, u.Username, u.Email, u.Name}, err)
}

func bookParam(w http.ResponseWriter, r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "book"), 10, 64)
	if err != nil {
		writeErr(w, model.ErrNotFound)
		return 0, err
	}
	return id, nil
}

func (a *api) actor(r *http.Request) (auth.Actor, error) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		return auth.Actor{}, http.ErrNoCookie
	}
	return actor, nil
}

// forceResync enqueues a resync of the caller's own principals; the dispatcher
// bumps their epochs out of band.
func (a *api) forceResync(w http.ResponseWriter, r *http.Request) {
	actor, err := a.actor(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	initiated := actor.UserID
	err = store.EnqueueEvent(r.Context(), a.pool, actor.UserID, "force_resync", map[string]any{}, &initiated)
	writeErr(w, err)
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, model.ErrNotFound)
		return 0, err
	}
	return id, nil
}

func decodeJSON(r *http.Request, v any) error {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return model.ErrPrecondition
	}
	return nil
}

type errBody struct {
	Error struct {
		Code  string `json:"code"`
		Field string `json:"field,omitempty"`
		Msg   string `json:"msg"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, err error) {
	if err == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	status := http.StatusInternalServerError
	code := "internal"
	switch {
	case errors.Is(err, model.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, model.ErrConflict),
		errors.Is(err, model.ErrUIDConflict),
		errors.Is(err, model.ErrFilenameRetired),
		errors.Is(err, model.ErrUsernameTaken),
		errors.Is(err, model.ErrEmailTaken):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, model.ErrPrecondition),
		errors.Is(err, model.ErrInvalidTiers):
		status, code = http.StatusBadRequest, "bad_request"
	}
	var body errBody
	body.Error.Code = code
	body.Error.Msg = err.Error()
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
