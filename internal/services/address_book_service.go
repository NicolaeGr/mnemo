package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"mnemo/internal/db"
	"mnemo/internal/models"
)

var (
	ErrAddressBookNotFound = errors.New("address book not found")
	ErrSlugTaken           = errors.New("you already have an address book with this slug")
)

type AddressBookService struct {
	store *db.Store
}

func NewAddressBookService(store *db.Store) *AddressBookService {
	return &AddressBookService{store: store}
}

func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	return s
}

func (s *AddressBookService) ListForUser(ctx context.Context, userID string) ([]*models.AddressBook, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT id, owner_id, slug, display_name FROM address_books
		 WHERE owner_id = ?
		 UNION
		 SELECT ab.id, ab.owner_id, ab.slug, ab.display_name
		 FROM address_books ab
		 JOIN address_book_permissions p ON p.address_book_id = ab.id
		 WHERE p.user_id = ?
		 ORDER BY display_name ASC`, userID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	defer rows.Close()

	var books []*models.AddressBook
	for rows.Next() {
		b := &models.AddressBook{}
		if err := rows.Scan(&b.ID, &b.OwnerID, &b.Slug, &b.DisplayName); err != nil {
			return nil, fmt.Errorf("scan book: %w", err)
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

func (s *AddressBookService) GetByID(ctx context.Context, id string) (*models.AddressBook, error) {
	b := &models.AddressBook{}
	err := s.store.DB.QueryRowContext(ctx,
		"SELECT id, owner_id, slug, display_name FROM address_books WHERE id = ?", id,
	).Scan(&b.ID, &b.OwnerID, &b.Slug, &b.DisplayName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAddressBookNotFound
		}
		return nil, fmt.Errorf("get book: %w", err)
	}
	return b, nil
}

func (s *AddressBookService) Create(ctx context.Context, ownerID, displayName string) (*models.AddressBook, error) {
	slug := slugify(displayName)
	if slug == "" {
		slug = "book"
	}

	var exists bool
	err := s.store.DB.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM address_books WHERE owner_id = ? AND slug = ?)", ownerID, slug,
	).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("check slug: %w", err)
	}
	if exists {
		return nil, ErrSlugTaken
	}

	id := generateID()
	_, err = s.store.DB.ExecContext(ctx,
		"INSERT INTO address_books (id, owner_id, slug, display_name) VALUES (?, ?, ?, ?)",
		id, ownerID, slug, displayName,
	)
	if err != nil {
		return nil, fmt.Errorf("create book: %w", err)
	}

	return &models.AddressBook{
		ID:          id,
		OwnerID:     ownerID,
		Slug:        slug,
		DisplayName: displayName,
	}, nil
}

func (s *AddressBookService) Update(ctx context.Context, id, ownerID, displayName string) error {
	slug := slugify(displayName)

	res, err := s.store.DB.ExecContext(ctx,
		"UPDATE address_books SET display_name = ?, slug = ? WHERE id = ? AND owner_id = ?",
		displayName, slug, id, ownerID,
	)
	if err != nil {
		return fmt.Errorf("update book: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAddressBookNotFound
	}
	return nil
}

func (s *AddressBookService) Delete(ctx context.Context, id, ownerID string) error {
	res, err := s.store.DB.ExecContext(ctx,
		"DELETE FROM address_books WHERE id = ? AND owner_id = ?", id, ownerID,
	)
	if err != nil {
		return fmt.Errorf("delete book: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAddressBookNotFound
	}
	return nil
}

func (s *AddressBookService) GetPermissions(ctx context.Context, bookID string) ([]*models.AddressBookPermission, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT p.address_book_id, p.user_id, p.access_level, u.username
		 FROM address_book_permissions p
		 JOIN users u ON u.id = p.user_id
		 WHERE p.address_book_id = ?`, bookID,
	)
	if err != nil {
		return nil, fmt.Errorf("get permissions: %w", err)
	}
	defer rows.Close()

	var perms []*models.AddressBookPermission
	for rows.Next() {
		p := &models.AddressBookPermission{}
		var username string
		if err := rows.Scan(&p.AddressBookID, &p.UserID, &p.AccessLevel, &username); err != nil {
			return nil, fmt.Errorf("scan perm: %w", err)
		}
		perms = append(perms, p)
	}
	return perms, rows.Err()
}

func (s *AddressBookService) SetPermission(ctx context.Context, bookID, userID, accessLevel string) error {
	_, err := s.store.DB.ExecContext(ctx,
		`INSERT INTO address_book_permissions (address_book_id, user_id, access_level)
		 VALUES (?, ?, ?)
		 ON CONFLICT(address_book_id, user_id) DO UPDATE SET access_level = ?`,
		bookID, userID, accessLevel, accessLevel,
	)
	return err
}

func (s *AddressBookService) RemovePermission(ctx context.Context, bookID, userID string) error {
	_, err := s.store.DB.ExecContext(ctx,
		"DELETE FROM address_book_permissions WHERE address_book_id = ? AND user_id = ?",
		bookID, userID,
	)
	return err
}
