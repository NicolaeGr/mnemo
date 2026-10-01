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
	err = store.WithTx(ctx, b.pool, a, func(s store.ScopedStore) error {
		visible, err := resolve.Resolver{}.VisibleBooks(ctx, s)
		if err != nil {
			return err
		}
		contacts, err := s.ListContactsInBooks(ctx, visibleIDs(visible))
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
	var dataReq carddav.AddressDataRequest
	if query != nil {
		dataReq = query.DataRequest
	}
	objects, err := b.ListAddressObjects(ctx, p, &dataReq)
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
	text := vcardmeta.CanonicalText(card)
	meta, err := json.Marshal(vcardmeta.SearchMeta(card))
	if err != nil {
		return nil, fmt.Errorf("marshal search meta: %w", err)
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
		res, err = s.PutContact(ctx, store.PutContactParams{
			Filename:   objPath,
			UID:        uid,
			VCardText:  text,
			SearchMeta: meta,
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
		deleted, err = s.DeleteContact(ctx, objPath)
		return err
	})
	if err != nil {
		return err
	}
	if !deleted {
		return b.notFound()
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
