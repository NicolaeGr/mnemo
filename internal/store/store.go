package store

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	PG *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pg, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pg.Ping(ctx); err != nil {
		return nil, err
	}
	return &Store{PG: pg}, nil
}

func (s *Store) Close() {
	s.PG.Close()
}
