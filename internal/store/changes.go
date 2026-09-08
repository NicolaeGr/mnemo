package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Change is one contact_changes row in the per-user sync stream.
type Change struct {
	ID         int64
	UserID     int64
	Filename   string
	ChangeType string
	ChangedAt  time.Time
}

// AppendChange inserts a row into the per-user change stream. Caller must hold
// the C2.3 lock for userID (LockSync).
func AppendChange(ctx context.Context, tx pgx.Tx, userID int64, filename, changeType string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO contact_changes (user_id, filename, change_type)
		VALUES ($1, $2, $3)`, userID, filename, changeType)
	return err
}

// MaxCommittedChangeID returns the largest contact_changes.id for the user, or
// 0 when the stream is empty. Used as the sync token tail and as getctag
// material.
func MaxCommittedChangeID(ctx context.Context, pool *pgxpool.Pool, userID int64) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM contact_changes WHERE user_id = $1`,
		userID).Scan(&id)
	return id, err
}

// StreamOptions bounds a sync-collection delta query.
type StreamOptions struct {
	AfterID int64
	Limit   int
}

// StreamDeltas scans contact_changes after AfterID in id order.
func (s ScopedStore) StreamDeltas(ctx context.Context, o StreamOptions) ([]Change, error) {
	rows, err := s.Tx.Query(ctx, `
		SELECT id, user_id, filename, change_type, changed_at
		  FROM contact_changes
		 WHERE user_id = $1 AND id > $2
		 ORDER BY id
		 LIMIT $3`, s.Actor.UserID, o.AfterID, o.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Change, 0)
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.ID, &c.UserID, &c.Filename, &c.ChangeType, &c.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LiveChangeSet collapses stream rows into {filename: highest id}, keeping the
// highest id per filename.
func LiveChangeSet(rows []Change) map[string]int64 {
	set := make(map[string]int64, len(rows))
	for _, c := range rows {
		if prev, ok := set[c.Filename]; !ok || c.ID > prev {
			set[c.Filename] = c.ID
		}
	}
	return set
}
