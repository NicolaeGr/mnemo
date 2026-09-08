// Package webdavsvc exposes Postgres contacts over CardDAV (RFC 6352).
package webdavsvc

import (
	"context"
	"strings"

	"github.com/emersion/go-webdav"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/auth"
	"example.com/segments/internal/store"
)

const (
	addressBookName = "Contacts"
	prefix          = "/carddav"
)

// The go-webdav carddav.Handler derives every resource type from PATH DEPTH
// relative to Prefix: depth 1 is a user principal, depth 2 the address-book
// home set, depth 3 an address book, depth 4+ an address object. The DAV
// namespace therefore nests the single address book (the system book, slug
// 'all') under the home set:
//
//	/carddav/{username}/                     user principal (depth 1)
//	/carddav/{username}/contacts/            address-book home set (depth 2)
//	/carddav/{username}/contacts/all/        the single address book (depth 3)
//	/carddav/{username}/contacts/all/{fn}    address object (depth 4)
//
// This is the closest the library allows to the gospel's /carddav/principals/
// {u}/ + /carddav/contacts/{u}/ layout: the principal stays one level above
// the home set, and ListAddressBooks returns exactly one entry.
func principalPath(username string) string {
	return prefix + "/" + username + "/"
}

func homeSetPath(username string) string {
	return principalPath(username) + "contacts/"
}

func addressBookPath(username string) string {
	return homeSetPath(username) + "all/"
}

type backend struct {
	users *store.Users
	pool  *pgxpool.Pool
}

func (b *backend) actor(ctx context.Context) (auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return auth.Actor{}, webdav.NewHTTPError(401, nil)
	}
	return a, nil
}

func (b *backend) pathSuffix(ctx context.Context, p string) (string, bool) {
	ab, err := b.addressBookPath(ctx)
	if err != nil {
		return "", false
	}
	if p == ab || p == ab+"/" {
		return "", true
	}
	return strings.TrimPrefix(p, ab), strings.HasPrefix(p, ab)
}

func (b *backend) addressBookPath(ctx context.Context) (string, error) {
	a, err := b.actor(ctx)
	if err != nil {
		return "", err
	}
	return addressBookPath(a.Username), nil
}

func (b *backend) notFound() error {
	return webdav.NewHTTPError(404, nil)
}
