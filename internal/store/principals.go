package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/segments/internal/model"
)

// Principal is one row of principals: a device/app/credential (§4.1).
type Principal struct {
	ID           int64
	UserID       int64
	Tier         string
	Label        string
	TokenHash    *string
	SyncEpoch    int64
	LastSyncedAt *time.Time
	CreatedAt    time.Time
}

// PrincipalStore groups the repo operations that only need the pool.
type PrincipalStore struct {
	pool *pgxpool.Pool
}

func NewPrincipals(pool *pgxpool.Pool) *PrincipalStore {
	return &PrincipalStore{pool: pool}
}

// IssueToken creates a principal with a fresh device token and returns the raw
// token exactly once (§4.1 / §9 principals POST). The raw token is never
// stored; only its sha256 hex hash is.
func (p *PrincipalStore) IssueToken(ctx context.Context, userID int64, tier, label string) (Principal, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Principal{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256Hex(token)

	var pr Principal
	row := p.pool.QueryRow(ctx, `
		INSERT INTO principals (user_id, tier, label, token_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, tier, label, token_hash, sync_epoch, last_synced_at, created_at`,
		userID, tier, label, hash)
	err := scanPrincipal(row, &pr)
	return pr, token, err
}

// ByTokenHash resolves a bearer credential to exactly one principal or none
// (I7). Not found → model.ErrNotFound.
func (p *PrincipalStore) ByTokenHash(ctx context.Context, tokenHash string) (Principal, error) {
	var pr Principal
	err := scanPrincipal(p.pool.QueryRow(ctx, `
		SELECT id, user_id, tier, label, token_hash, sync_epoch, last_synced_at, created_at
		  FROM principals
		 WHERE token_hash = $1`, tokenHash), &pr)
	return pr, err
}

func (p *PrincipalStore) ListForUser(ctx context.Context, userID int64) ([]Principal, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, user_id, tier, label, token_hash, sync_epoch, last_synced_at, created_at
		  FROM principals
		 WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Principal, 0)
	for rows.Next() {
		var pr Principal
		if err := scanPrincipal(rows, &pr); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

func scanPrincipal(row pgx.Row, pr *Principal) error {
	err := row.Scan(&pr.ID, &pr.UserID, &pr.Tier, &pr.Label, &pr.TokenHash,
		&pr.SyncEpoch, &pr.LastSyncedAt, &pr.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrNotFound
	}
	return err
}

// TokenHashOf hashes a raw device token for storage/lookup (C2.6).
func TokenHashOf(token string) string {
	return sha256Hex(token)
}

// BumpPrincipalsEpoch atomically advances sync_epoch for the given principal
// ids inside tx (caller holds the C2.3 lock) using the §5.6 primitive. Returns
// the new epoch value.
func BumpPrincipalsEpoch(ctx context.Context, tx pgx.Tx, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var newEpoch int64
	err := tx.QueryRow(ctx, `
		UPDATE principals SET sync_epoch =
			COALESCE((SELECT MAX(sync_epoch) FROM principals) + 1, 1)
		WHERE id = ANY($1)
		RETURNING sync_epoch`, ids).Scan(&newEpoch)
	return newEpoch, err
}
