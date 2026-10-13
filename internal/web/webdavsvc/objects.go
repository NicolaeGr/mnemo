package webdavsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/resolve"
	"github.com/nicolaegr/mnemo/internal/store"
	vcardmeta "github.com/nicolaegr/mnemo/internal/vcard"
)

// rawBodyKey carries the raw request body of a PUT so PutAddressObject can store
// a byte-level normalization of what the client sent instead of a structural
// re-encode of the card the go-webdav handler parsed for us.
type rawBodyKey struct{}

func withRawBody(ctx context.Context, b []byte) context.Context {
	return context.WithValue(ctx, rawBodyKey{}, b)
}

func rawBody(ctx context.Context) ([]byte, bool) {
	b, ok := ctx.Value(rawBodyKey{}).([]byte)
	return b, ok
}

func (b *backend) GetAddressObject(ctx context.Context, p string, _ *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	objPath, ok := b.pathSuffix(ctx, p)
	if !ok || objPath == "" {
		return nil, b.notFound()
	}
	a, err := b.actor(ctx)
	if err != nil {
		return nil, err
	}
	abPath, err := b.addressBookPath(ctx)
	if err != nil {
		return nil, err
	}
	var obj *carddav.AddressObject
	err = store.WithTx(ctx, b.pool, a, func(s store.ScopedStore) error {
		c, err := s.LiveContactByFilename(ctx, objPath)
		if err != nil {
			return err
		}
		visible, err := resolve.Resolver{}.VisibleBooks(ctx, s)
		if err != nil {
			return err
		}
		bookIDs, err := s.ContactBookIDs(ctx, c.ID)
		if err != nil {
			return err
		}
		if !anyVisible(bookIDs, visible) { // a card with no visible book is not found
			return model.ErrNotFound
		}
		card, err := vcard.NewDecoder(bytes.NewBufferString(c.VCardText)).Decode()
		if err != nil {
			return err
		}
		obj = &carddav.AddressObject{
			Path:    path.Join(abPath, c.Filename),
			ModTime: c.UpdatedAt,
			ETag:    c.ETag,
			Card:    card,
		}
		return nil
	})
	if errors.Is(err, model.ErrNotFound) {
		return nil, b.notFound()
	}
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (b *backend) ListAddressObjects(ctx context.Context, p string, _ *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	if _, ok := b.pathSuffix(ctx, p); !ok {
		return nil, b.notFound()
	}
	abPath, err := b.addressBookPath(ctx)
	if err != nil {
		return nil, err
	}
	a, err := b.actor(ctx)
	if err != nil {
		return nil, err
	}
	objects := make([]carddav.AddressObject, 0)
	wantData := wantsCards(ctx)
	err = store.WithTx(ctx, b.pool, a, func(s store.ScopedStore) error {
		visible, err := resolve.Resolver{}.VisibleBooks(ctx, s)
		if err != nil {
			return err
		}
		ids := visibleIDs(visible)
		if !wantData {
			// The caller established this listing needs no card bodies (an
			// etag-only depth-1 PROPFIND), so skip loading and decoding every
			// body: SELECT filename, etag, updated_at.
			refs, err := s.ListVisibleObjectRefs(ctx, ids)
			if err != nil {
				return err
			}
			for _, r := range refs {
				objects = append(objects, carddav.AddressObject{
					Path:    path.Join(abPath, r.Filename),
					ModTime: r.UpdatedAt,
					ETag:    r.ETag,
				})
			}
			return nil
		}
		contacts, err := s.ListContactsInBooks(ctx, ids)
		if err != nil {
			return err
		}
		for _, c := range contacts {
			card, err := vcard.NewDecoder(bytes.NewBufferString(c.VCardText)).Decode()
			if err != nil {
				continue
			}
			objects = append(objects, carddav.AddressObject{
				Path:    path.Join(abPath, c.Filename),
				ModTime: c.UpdatedAt,
				ETag:    c.ETag,
				Card:    card,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return objects, nil
}

func (b *backend) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	// Query results may carry address-data, and the caller's filters are applied
	// to decoded cards, so the listing always carries bodies here. wantsCards is
	// only ever cleared for the depth-1 PROPFIND listing.
	objects, err := b.ListAddressObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	if query != nil && len(query.PropFilters) > 0 {
		return carddav.Filter(query, objects)
	}
	return objects, nil
}

func (b *backend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	objPath, ok := b.pathSuffix(ctx, p)
	if !ok || objPath == "" {
		return nil, b.notFound()
	}
	a, err := b.actor(ctx)
	if err != nil {
		return nil, err
	}

	vcardmeta.EnsureFormattedName(card)
	uid := vcardmeta.EnsureUID(card, objPath)

	var text string
	var meta []byte
	if raw, ok := rawBody(ctx); ok {
		// Byte-level canonicalization of the client's own bytes; FN/UID are
		// spliced in only when the client omitted them.
		text, uid, meta, err = vcardmeta.FromRaw(string(raw), objPath)
		if err != nil {
			return nil, webdav.NewHTTPError(http.StatusBadRequest, err)
		}
	} else {
		// No raw body captured (defensive: the filter always supplies one).
		text = vcardmeta.CanonicalText(card)
		if text == "" {
			return nil, webdav.NewHTTPError(http.StatusBadRequest, nil)
		}
		meta, err = json.Marshal(vcardmeta.SearchMeta(card))
		if err != nil {
			return nil, fmt.Errorf("marshal search meta: %w", err)
		}
	}
	cond, err := putPrecondition(opts)
	if err != nil {
		return nil, err
	}

	var res store.PutResult
	txErr := store.WithTx(ctx, b.pool, a, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, a.UserID); err != nil {
			return err
		}
		if err := b.ensureVisible(ctx, s, objPath); err != nil {
			return err
		}
		res, err = s.PutContact(ctx, store.PutContactParams{
			Filename:   objPath,
			UID:        uid,
			VCardText:  text,
			SearchMeta: meta,
			DAV:        true,
		}, cond)
		return err
	})
	switch {
	case errors.Is(txErr, model.ErrPrecondition):
		return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, txErr)
	case errors.Is(txErr, model.ErrNotFound):
		return nil, b.notFound()
	case errors.Is(txErr, model.ErrUIDConflict), errors.Is(txErr, model.ErrFilenameRetired):
		return nil, webdav.NewHTTPError(http.StatusConflict, txErr)
	case txErr != nil:
		return nil, txErr
	}

	// go-webdav quotes the ETag on the wire and clients unquote once, so the raw
	// hex stored here is the correct value to return.
	return &carddav.AddressObject{Path: p, ETag: res.ETag, Card: card}, nil
}

func (b *backend) DeleteAddressObject(ctx context.Context, p string) error {
	objPath, ok := b.pathSuffix(ctx, p)
	if !ok || objPath == "" {
		return b.notFound()
	}
	a, err := b.actor(ctx)
	if err != nil {
		return err
	}
	var deleted bool
	err = store.WithTx(ctx, b.pool, a, func(s store.ScopedStore) error {
		if err := store.LockSync(ctx, s.Tx, a.UserID); err != nil {
			return err
		}
		if err := b.ensureVisible(ctx, s, objPath); err != nil {
			return err
		}
		deleted, err = s.DeleteContact(ctx, objPath)
		return err
	})
	if errors.Is(err, model.ErrNotFound) {
		return b.notFound()
	}
	if err != nil {
		return err
	}
	if !deleted {
		return b.notFound()
	}
	return nil
}

// ensureVisible returns ErrNotFound when a live contact with this filename
// exists but the actor cannot see it. C2.4 makes an invisible card behave like a
// missing one, so PUT and DELETE call this before writing, the same way the read
// paths do. A filename with no live contact returns nil; the precondition and
// insert paths handle that case.
func (b *backend) ensureVisible(ctx context.Context, s store.ScopedStore, filename string) error {
	c, err := s.LiveContactByFilename(ctx, filename)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	visible, err := resolve.Resolver{}.VisibleBooks(ctx, s)
	if err != nil {
		return err
	}
	books, err := s.ContactBookIDs(ctx, c.ID)
	if err != nil {
		return err
	}
	if !anyVisible(books, visible) {
		return model.ErrNotFound
	}
	return nil
}

// putPrecondition translates the library's conditional headers into the store's
// precondition model. Unparseable If-Match etags are a 400.
func putPrecondition(opts *carddav.PutAddressObjectOptions) (store.Precondition, error) {
	if opts == nil {
		return store.Precondition{}, nil
	}
	var cond store.Precondition
	if opts.IfNoneMatch.IsSet() && opts.IfNoneMatch.IsWildcard() {
		cond.IfNoneMatchAll = true
	}
	if opts.IfMatch.IsSet() {
		if !opts.IfMatch.IsWildcard() {
			if etag, err := opts.IfMatch.ETag(); err == nil {
				cond.IfMatch = &etag
			} else {
				return cond, webdav.NewHTTPError(http.StatusBadRequest, nil)
			}
		}
	}
	return cond, nil
}

func visibleIDs(visible map[int64]bool) []int64 {
	ids := make([]int64, 0, len(visible))
	for id, ok := range visible {
		if ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func anyVisible(bookIDs []int64, visible map[int64]bool) bool {
	for _, id := range bookIDs {
		if visible[id] {
			return true
		}
	}
	return false
}
