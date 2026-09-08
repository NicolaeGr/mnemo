package webdavsvc

import (
	"context"
	"net/http"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"
)

func (b *backend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	a, err := b.actor(ctx)
	if err != nil {
		return "", err
	}
	return homeSetPath(a.Username), nil
}

func (b *backend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	ab, err := b.addressBook(ctx)
	if err != nil {
		return nil, err
	}
	return []carddav.AddressBook{ab}, nil
}

func (b *backend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	ab, err := b.addressBook(ctx)
	if err != nil {
		return nil, err
	}
	if p != ab.Path {
		return nil, b.notFound()
	}
	return &ab, nil
}

func (b *backend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *backend) DeleteAddressBook(ctx context.Context, p string) error {
	return webdav.NewHTTPError(http.StatusForbidden, nil)
}

func (b *backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	a, err := b.actor(ctx)
	if err != nil {
		return "", err
	}
	return principalPath(a.Username), nil
}

func (b *backend) addressBook(ctx context.Context) (carddav.AddressBook, error) {
	a, err := b.actor(ctx)
	if err != nil {
		return carddav.AddressBook{}, err
	}

	displayName := a.Username
	if u, err := b.users.ByID(ctx, a.UserID); err == nil && u.Name != "" {
		displayName = u.Name
	}

	return carddav.AddressBook{
		Path: addressBookPath(a.Username),
		Name: displayName,
		SupportedAddressData: []carddav.AddressDataType{
			{ContentType: vcard.MIMEType, Version: "3.0"},
		},
	}, nil
}
