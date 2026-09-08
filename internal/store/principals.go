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

// Principal is one row of principals: a device/app/credential.
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
// token, shown to the client exactly once; only its hash is stored.
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

// ByTokenHash resolves a bearer credential to exactly one principal or none.
// Not found → model.ErrNotFound.
func (p *PrincipalStore) ByTokenHash(ctx context.Context, tokenHash string) (Principal, error) {
	var pr Principal
	err := scanPrincipal(p.pool.QueryRow(ctx, `
		SELECT id, user_id, tier, label, token_hash, sync_epoch, last_synced_at, created_at
		  FROM principals
		 WHERE token_hash = $1`, tokenHash), &pr)
	return pr, err
}

// Touch updates last_synced_at at most once per five minutes per principal.
// Diagnostic only; callers ignore the error.
func (p *PrincipalStore) Touch(ctx context.Context, id int64) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE principals SET last_synced_at = now()
		 WHERE id = $1
		   AND (last_synced_at IS NULL OR last_synced_at < now() - interval '5 minutes')`, id)
	return err
}

// PrincipalEpoch returns the actor's sync_epoch, or 0 when the actor is a
// password account (no principal, epoch ignored).
func (s ScopedStore) PrincipalEpoch(ctx context.Context) (int64, error) {
	if s.Actor.PrincipalID == nil {
		return 0, nil
	}
	var epoch int64
	err := s.Tx.QueryRow(ctx, `
		SELECT sync_epoch FROM principals
		 WHERE id = $1 AND user_id = $2`, *s.Actor.PrincipalID, s.Actor.UserID).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, model.ErrNotFound
	}
	return epoch, err
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
// ids inside the current tx (caller holds the sync lock). Returns the new
// epoch value.
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

// PrincipalInfo is the visibility-relevant slice of the acting principal:
// its tier ("" for password auth, i.e. tier-all) and its per-book overrides.
// Overrides is non-nil only for a token-backed principal.
type PrincipalInfo struct {
	Tier      string
	Overrides map[int64]bool
}

func (s ScopedStore) PrincipalInfo(ctx context.Context) (PrincipalInfo, error) {
	var info PrincipalInfo
	if s.Actor.PrincipalID == nil {
		return info, nil
	}
	pid := *s.Actor.PrincipalID
	if err := s.Tx.QueryRow(ctx, `
		SELECT tier FROM principals
		 WHERE id = $1 AND user_id = $2`, pid, s.Actor.UserID).Scan(&info.Tier); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PrincipalInfo{}, model.ErrNotFound
		}
		return PrincipalInfo{}, err
	}

	info.Overrides = make(map[int64]bool)
	rows, err := s.Tx.Query(ctx, `
		SELECT o.book_id, o.enabled
		  FROM principal_book_overrides o
		  JOIN principals p ON p.id = o.principal_id
		 WHERE p.id = $1 AND p.user_id = $2`, pid, s.Actor.UserID)
	if err != nil {
		return PrincipalInfo{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var bookID int64
		var enabled bool
		if err := rows.Scan(&bookID, &enabled); err != nil {
			return PrincipalInfo{}, err
		}
		info.Overrides[bookID] = enabled
	}
	return info, rows.Err()
}
