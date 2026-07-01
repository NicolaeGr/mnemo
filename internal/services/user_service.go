package services

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"mnemo/internal/db"
	"mnemo/internal/models"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserNotFound  = errors.New("user not found")
	ErrUsernameTaken = errors.New("username already exists")
	ErrInvalidCreds  = errors.New("invalid username or password")
	ErrUserInactive  = errors.New("user account is inactive")
	ErrNotAuthorized = errors.New("not authorized")
)

type UserService struct {
	store *db.Store
}

func NewUserService(store *db.Store) *UserService {
	return &UserService{store: store}
}

func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random id: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func (s *UserService) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := s.store.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

func (s *UserService) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	u := &models.User{}
	var isActive, isTmpPw int
	var createdAt string
	err := s.store.DB.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, is_active, is_temporary_password,
		        password_reset_token, password_reset_expires_at,
		        default_address_book_id, created_at
		 FROM users WHERE id = ?`, id,
	).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role,
		&isActive, &isTmpPw,
		&u.PasswordResetToken, &u.PasswordResetExpiresAt,
		&u.DefaultAddressBookID, &createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	u.IsActive = isActive == 1
	u.IsTemporaryPassword = isTmpPw == 1
	u.CreatedAt, _ = time.Parse(time.DateTime, createdAt)
	return u, nil
}

func (s *UserService) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	u := &models.User{}
	var isActive, isTmpPw int
	var createdAt string
	err := s.store.DB.QueryRowContext(ctx,
		`SELECT id, username, password_hash, role, is_active, is_temporary_password,
		        password_reset_token, password_reset_expires_at,
		        default_address_book_id, created_at
		 FROM users WHERE username = ?`, username,
	).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role,
		&isActive, &isTmpPw,
		&u.PasswordResetToken, &u.PasswordResetExpiresAt,
		&u.DefaultAddressBookID, &createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	u.IsActive = isActive == 1
	u.IsTemporaryPassword = isTmpPw == 1
	u.CreatedAt, _ = time.Parse(time.DateTime, createdAt)
	return u, nil
}

func (s *UserService) ListUsers(ctx context.Context) ([]*models.User, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT id, username, role, is_active, created_at
		 FROM users ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []*models.User
	for rows.Next() {
		u := &models.User{}
		var isActive int
		var createdAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &isActive, &createdAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		u.IsActive = isActive == 1
		u.CreatedAt, _ = time.Parse(time.DateTime, createdAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *UserService) CreateUser(ctx context.Context, username, password string, isAdmin bool) (*models.User, error) {
	var exists bool
	err := s.store.DB.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM users WHERE username = ?)", username,
	).Scan(&exists)
	if err != nil {
		return nil, fmt.Errorf("check existing username: %w", err)
	}
	if exists {
		return nil, ErrUsernameTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	role := "user"
	if isAdmin {
		role = "admin"
	}

	id := generateID()
	_, err = s.store.DB.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, role, is_active)
		 VALUES (?, ?, ?, ?, 1)`,
		id, username, string(hash), role,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	return &models.User{
		ID:       id,
		Username: username,
		Role:     role,
		IsActive: true,
	}, nil
}

func (s *UserService) AuthenticateUser(ctx context.Context, username, password string) (*models.User, error) {
	user, err := s.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCreds
		}
		return nil, err
	}

	if !user.IsActive {
		return nil, ErrUserInactive
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCreds
	}

	return user, nil
}

func (s *UserService) ToggleUserActive(ctx context.Context, userID string) (*models.User, error) {
	user, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}

	newActive := 0
	if !user.IsActive {
		newActive = 1
	}

	_, err = s.store.DB.ExecContext(ctx,
		"UPDATE users SET is_active = ? WHERE id = ?", newActive, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("toggle active: %w", err)
	}

	user.IsActive = !user.IsActive
	return user, nil
}

func (s *UserService) CreateInvite(ctx context.Context, createdByID string, expiresIn time.Duration) (*models.UserInvite, error) {
	token := generateID()
	expiresAt := time.Now().Add(expiresIn)

	_, err := s.store.DB.ExecContext(ctx,
		`INSERT INTO user_invites (token, created_by, expires_at)
		 VALUES (?, ?, ?)`,
		token, createdByID, expiresAt.Format(time.DateTime),
	)
	if err != nil {
		return nil, fmt.Errorf("create invite: %w", err)
	}

	return &models.UserInvite{
		Token:     token,
		CreatedBy: createdByID,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}, nil
}

func (s *UserService) GetInvite(ctx context.Context, token string) (*models.UserInvite, error) {
	inv := &models.UserInvite{}
	var expiresAt, createdAt string
	err := s.store.DB.QueryRowContext(ctx,
		`SELECT token, created_by, used_by, expires_at, created_at
		 FROM user_invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.CreatedBy, &inv.UsedBy, &expiresAt, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("invite not found")
		}
		return nil, fmt.Errorf("get invite: %w", err)
	}

	inv.ExpiresAt, _ = time.Parse(time.DateTime, expiresAt)
	inv.CreatedAt, _ = time.Parse(time.DateTime, createdAt)

	if time.Now().After(inv.ExpiresAt) {
		return nil, fmt.Errorf("invite has expired")
	}
	if inv.UsedBy != nil {
		return nil, fmt.Errorf("invite has already been used")
	}

	return inv, nil
}

func (s *UserService) UseInvite(ctx context.Context, token, userID string) error {
	res, err := s.store.DB.ExecContext(ctx,
		"UPDATE user_invites SET used_by = ? WHERE token = ?", userID, token,
	)
	if err != nil {
		return fmt.Errorf("use invite: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("invite not found")
	}
	return nil
}

func (s *UserService) ListInvites(ctx context.Context, createdByID string) ([]*models.UserInvite, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT token, created_by, used_by, expires_at, created_at
		 FROM user_invites WHERE created_by = ? ORDER BY created_at DESC`, createdByID,
	)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()

	var invites []*models.UserInvite
	for rows.Next() {
		inv := &models.UserInvite{}
		var expiresAt, createdAt string
		if err := rows.Scan(&inv.Token, &inv.CreatedBy, &inv.UsedBy, &expiresAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan invite: %w", err)
		}
		inv.ExpiresAt, _ = time.Parse(time.DateTime, expiresAt)
		inv.CreatedAt, _ = time.Parse(time.DateTime, createdAt)
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

func (s *UserService) CreateDeviceToken(ctx context.Context, userID, deviceName string) (*models.DeviceToken, string, error) {
	rawToken := generateID() + generateID()

	hash, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", fmt.Errorf("hash token: %w", err)
	}

	id := generateID()
	_, err = s.store.DB.ExecContext(ctx,
		`INSERT INTO user_device_tokens (id, user_id, device_name, token_hash)
		 VALUES (?, ?, ?, ?)`,
		id, userID, deviceName, string(hash),
	)
	if err != nil {
		return nil, "", fmt.Errorf("insert device token: %w", err)
	}

	return &models.DeviceToken{
		ID:         id,
		UserID:     userID,
		DeviceName: deviceName,
		CreatedAt:  time.Now(),
	}, rawToken, nil
}

func (s *UserService) ListDeviceTokens(ctx context.Context, userID string) ([]*models.DeviceToken, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT id, user_id, device_name, last_used_at, last_seen_ip, created_at
		 FROM user_device_tokens WHERE user_id = ? ORDER BY created_at DESC`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list device tokens: %w", err)
	}
	defer rows.Close()

	var tokens []*models.DeviceToken
	for rows.Next() {
		t := &models.DeviceToken{}
		var lastUsedAt, createdAt *string
		if err := rows.Scan(&t.ID, &t.UserID, &t.DeviceName, &lastUsedAt, &t.LastSeenIP, &createdAt); err != nil {
			return nil, fmt.Errorf("scan device token: %w", err)
		}
		if lastUsedAt != nil {
			parsed, _ := time.Parse(time.DateTime, *lastUsedAt)
			t.LastUsedAt = &parsed
		}
		if createdAt != nil {
			parsed, _ := time.Parse(time.DateTime, *createdAt)
			t.CreatedAt = parsed
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *UserService) DeleteDeviceToken(ctx context.Context, tokenID, userID string) error {
	res, err := s.store.DB.ExecContext(ctx,
		"DELETE FROM user_device_tokens WHERE id = ? AND user_id = ?", tokenID, userID,
	)
	if err != nil {
		return fmt.Errorf("delete device token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("device token not found")
	}
	return nil
}

func (s *UserService) AuthenticateDeviceToken(ctx context.Context, userID, rawToken string) (bool, error) {
	rows, err := s.store.DB.QueryContext(ctx,
		`SELECT id, token_hash FROM user_device_tokens WHERE user_id = ?`, userID,
	)
	if err != nil {
		return false, fmt.Errorf("query tokens: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return false, fmt.Errorf("scan token: %w", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(rawToken)); err == nil {
			now := time.Now().Format(time.DateTime)
			s.store.DB.ExecContext(ctx,
				"UPDATE user_device_tokens SET last_used_at = ? WHERE id = ?", now, id,
			)
			return true, nil
		}
	}
	return false, nil
}
