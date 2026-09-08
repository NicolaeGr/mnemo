// Package api exposes the minimal REST surface that drives book and tag
// mutations, so device principals actually see epoch bumps. Auth is the same
// Bearer-or-Basic scheme as the DAV plane; every request acts as the resolved
// user.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/auth"
	"example.com/segments/internal/model"
	"example.com/segments/internal/store"
)

type api struct {
	users *store.Users
	pool  *pgxpool.Pool
}

// New builds the /api/v1 handler.
func New(users *store.Users, pool *pgxpool.Pool) http.Handler {
	a := &api{users: users, pool: pool}
	r := chi.NewRouter()

	r.Post("/principals", a.createPrincipal)
	r.Get("/principals", a.listPrincipals)

	r.Route("/books", func(r chi.Router) {
		r.Get("/", a.listBooks)
		r.Post("/", a.createBook)
		r.Patch("/{id}", a.updateBook)
		r.Delete("/{id}", a.deleteBook)
	})

	r.Route("/contacts", func(r chi.Router) {
		r.Get("/", a.searchContacts)
		r.Post("/{id}/tags", a.tagContact)
	})

	return auth.RequireDAV(users, store.NewPrincipals(pool), r)
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
			out = append(out, bookOut{b.ID, b.Slug, b.DisplayName, b.IsActive, b.SyncedTiers})
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
	writeJSON(w, 201, bookOut{book.ID, book.Slug, book.DisplayName, book.IsActive, book.SyncedTiers}, err)
}

type bookPatch struct {
	Active      *bool    `json:"active"`
	SyncedTiers []string `json:"synced_tiers"`
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
			return s.SetBookTiers(r.Context(), id, body.SyncedTiers)
		}
		return nil
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
	var result struct {
		Contacts []contactOut `json:"contacts"`
		NextPage bool         `json:"next_page"`
	}
	err = store.WithTx(r.Context(), a.pool, actor, func(s store.ScopedStore) error {
		res, err := s.Search(r.Context(), store.SearchParams{Query: q, Page: page})
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

func (a *api) actor(r *http.Request) (auth.Actor, error) {
	actor, ok := auth.ActorFrom(r.Context())
	if !ok {
		return auth.Actor{}, http.ErrNoCookie
	}
	return actor, nil
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
	case errors.Is(err, model.ErrConflict), errors.Is(err, model.ErrUIDConflict), errors.Is(err, model.ErrFilenameRetired):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, model.ErrPrecondition):
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
