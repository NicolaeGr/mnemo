package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/model"
)

const (
	InviteContactShare = "contact_share"
	InviteGroup        = "group_invite"
	InviteNetwork      = "network_request"
)

type Invitation struct {
	ID           int64
	Token        string
	Kind         string
	ResourceID   int64
	InviterID    int64
	InviterName  string
	InviteeID    *int64
	InviteeEmail *string
	Status       string
	CreatedAt    time.Time
}

// NetworkMember is one user in the viewer's network.
type NetworkMember struct {
	ID       int64
	Username string
	Name     string
	Email    string
}

const invitationColumns = `
	i.id, i.token, i.kind, i.resource_id, i.inviter_user_id,
	coalesce(nullif(u.name, ''), u.username::text),
	i.invitee_user_id, i.invitee_email, i.status, i.created_at`

const invitationFrom = `
	  FROM invitations i
	  JOIN users u ON u.id = i.inviter_user_id`

// CreateInvite mints a token for a share, group invite, or network request.
// An empty email stores no invitee address.
func CreateInvite(ctx context.Context, pool *pgxpool.Pool, inviterID int64, kind string, resourceID int64, email string) (Invitation, error) {
	token, err := newInviteToken()
	if err != nil {
		return Invitation{}, err
	}
	var emailArg *string
	if e := strings.ToLower(strings.TrimSpace(email)); e != "" {
		emailArg = &e
	}
	return scanInvitation(pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO invitations (token, kind, resource_id, inviter_user_id, invitee_email)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING *
		)
		SELECT `+invitationColumns+` FROM ins i JOIN users u ON u.id = i.inviter_user_id`,
		token, kind, resourceID, inviterID, emailArg))
}

func InviteByToken(ctx context.Context, pool *pgxpool.Pool, token string) (Invitation, error) {
	return scanInvitation(pool.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`
		 WHERE i.token = $1`, token))
}

// AcceptInvite binds the invitation to userID, marks it accepted, and adds each
// party to the other's network. Repeating an accepted invitation is a no-op.
func AcceptInvite(ctx context.Context, pool *pgxpool.Pool, token string, userID int64) (Invitation, error) {
	var inv Invitation
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		inv, err = scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`
			 WHERE i.token = $1 FOR UPDATE OF i`, token))
		if err != nil {
			return err
		}
		switch {
		case inv.InviteeID != nil && *inv.InviteeID != userID:
			return model.ErrNotFound
		case inv.InviterID == userID:
			return model.ErrConflict
		case inv.Status == "pending":
			if _, err := tx.Exec(ctx, `
				UPDATE invitations SET status = 'accepted', invitee_user_id = $2
				 WHERE id = $1 AND status = 'pending'`, inv.ID, userID); err != nil {
				return err
			}
		case inv.Status != "accepted":
			return model.ErrConflict
		}
		if err := addNetworkPair(ctx, tx, inv.InviterID, userID); err != nil {
			return err
		}
		inv.Status = "accepted"
		inv.InviteeID = &userID
		return nil
	})
	return inv, err
}

// DeclineInvite revokes a pending invitation addressed to userID.
func DeclineInvite(ctx context.Context, pool *pgxpool.Pool, token string, userID int64) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		inv, err := scanInvitation(tx.QueryRow(ctx, `SELECT `+invitationColumns+invitationFrom+`
			 WHERE i.token = $1 FOR UPDATE OF i`, token))
		if err != nil {
			return err
		}
		if inv.InviteeID != nil && *inv.InviteeID != userID {
			return model.ErrNotFound
		}
		if inv.Status != "pending" {
			return model.ErrConflict
		}
		_, err = tx.Exec(ctx,
			`UPDATE invitations SET status = 'revoked', invitee_user_id = $2 WHERE id = $1`, inv.ID, userID)
		return err
	})
}

// PendingInvites lists invitations addressed to the user by id or email.
func PendingInvites(ctx context.Context, pool *pgxpool.Pool, userID int64, email string) ([]Invitation, error) {
	rows, err := pool.Query(ctx, `SELECT `+invitationColumns+invitationFrom+`
		 WHERE i.status = 'pending'
		   AND i.inviter_user_id <> $1
		   AND (i.invitee_user_id = $1 OR lower(i.invitee_email::text) = lower($2))
		 ORDER BY i.created_at DESC`, userID, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Invitation, 0)
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// NetworkHas reports whether the owner's network already contains memberID.
func NetworkHas(ctx context.Context, pool *pgxpool.Pool, ownerID, memberID int64) (bool, error) {
	var found bool
	err := pool.QueryRow(ctx, `
		SELECT coalesce((SELECT network @> to_jsonb($2::bigint)
		                   FROM user_settings WHERE user_id = $1), false)`,
		ownerID, memberID).Scan(&found)
	return found, err
}

func NetworkMembers(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]NetworkMember, error) {
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.username::text, u.name, u.email
		  FROM user_settings s
		  CROSS JOIN LATERAL jsonb_array_elements_text(s.network) AS n(id)
		  JOIN users u ON u.id = n.id::bigint
		 WHERE s.user_id = $1
		 ORDER BY coalesce(nullif(u.name, ''), u.username::text), u.username`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NetworkMember, 0)
	for rows.Next() {
		var m NetworkMember
		if err := rows.Scan(&m.ID, &m.Username, &m.Name, &m.Email); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func addNetworkPair(ctx context.Context, tx pgx.Tx, a, b int64) error {
	if err := addNetwork(ctx, tx, a, b); err != nil {
		return err
	}
	return addNetwork(ctx, tx, b, a)
}

func addNetwork(ctx context.Context, tx pgx.Tx, ownerID, memberID int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_settings (user_id, network)
		VALUES ($1, jsonb_build_array($2::bigint))
		ON CONFLICT (user_id) DO UPDATE
		   SET network = CASE WHEN user_settings.network @> to_jsonb($2::bigint)
		                      THEN user_settings.network
		                      ELSE user_settings.network || to_jsonb($2::bigint) END,
		       updated_at = now()`,
		ownerID, memberID)
	return err
}

func newInviteToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInvitation(row rowScanner) (Invitation, error) {
	var inv Invitation
	err := row.Scan(&inv.ID, &inv.Token, &inv.Kind, &inv.ResourceID, &inv.InviterID,
		&inv.InviterName, &inv.InviteeID, &inv.InviteeEmail, &inv.Status, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invitation{}, model.ErrNotFound
	}
	return inv, err
}
