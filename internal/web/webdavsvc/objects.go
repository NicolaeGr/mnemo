package webdavsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"example.com/segments/internal/model"
	"example.com/segments/internal/store"
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
		contacts, err := s.ListLiveContacts(ctx)
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

	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, err
	}
	text := normalizeVCards(buf.String())

	meta, err := json.Marshal(buildSearchMeta(card))
	if err != nil {
		return nil, fmt.Errorf("webdavsvc: marshal search meta: %w", err)
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
			UID:        deriveUID(card, objPath),
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

	// Library quotes ao.ETag on the wire (internal.ETag.String) and the client
	// unquotes once; the raw hex (as stored, I3) is the correct value here.
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

// putPrecondition translates the library's conditional headers into the
// store's §5.3 precondition model. Unparseable If-Match etags are a 400.
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

func deriveUID(card vcard.Card, filename string) string {
	if uid := card.Value(vcard.FieldUID); uid != "" {
		return uid
	}
	if i := strings.LastIndex(filename, ".vcf"); i > 0 {
		return filename[:i]
	}
	return filename
}

func normalizeVCards(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\r' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func buildSearchMeta(card vcard.Card) map[string]any {
	meta := make(map[string]any)
	if fn := card.Value(vcard.FieldFormattedName); fn != "" {
		meta["fn"] = fn
	}
	if tels := card.Values(vcard.FieldTelephone); len(tels) > 0 {
		meta["tel"] = tels
	}
	if emails := card.Values(vcard.FieldEmail); len(emails) > 0 {
		meta["email"] = emails
	}
	return meta
}
