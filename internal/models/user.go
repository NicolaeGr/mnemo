package models

import "time"

type User struct {
	ID                     string     `json:"id"`
	Username               string     `json:"username"`
	PasswordHash           string     `json:"-"`
	Role                   string     `json:"role"`
	IsActive               bool       `json:"is_active"`
	IsTemporaryPassword    bool       `json:"is_temporary_password"`
	PasswordResetToken     *string    `json:"password_reset_token,omitempty"`
	PasswordResetExpiresAt *time.Time `json:"password_reset_expires_at,omitempty"`
	DefaultAddressBookID   *string    `json:"default_address_book_id,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
}

type UserInvite struct {
	Token     string    `json:"token"`
	CreatedBy string    `json:"created_by"`
	UsedBy    *string   `json:"used_by,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type AddressBook struct {
	ID          string `json:"id"`
	OwnerID     string `json:"owner_id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type AddressBookPermission struct {
	AddressBookID string `json:"address_book_id"`
	UserID        string `json:"user_id"`
	AccessLevel   string `json:"access_level"`
}

type AddressBookSubscription struct {
	UserID        string `json:"user_id"`
	AddressBookID string `json:"address_book_id"`
	SyncEnabled   bool   `json:"sync_enabled"`
}

type DeviceToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	DeviceName string     `json:"device_name"`
	TokenHash  string     `json:"-"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastSeenIP *string    `json:"last_seen_ip,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Contact struct {
	ID            string     `json:"id"`
	AddressBookID string     `json:"address_book_id"`
	DisplayName   string     `json:"display_name"`
	VCardText     string     `json:"vcard_text"`
	ETag          string     `json:"etag"`
	LastModified  time.Time  `json:"last_modified"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	FirstPhone    string     `json:"first_phone,omitempty"`
}
