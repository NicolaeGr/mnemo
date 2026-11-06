package webdavsvc_test

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicolaegr/mnemo/internal/auth"
	"github.com/nicolaegr/mnemo/internal/store"
)

func testGate(users *store.Users, pool *pgxpool.Pool) *auth.Gate {
	return auth.Config{}.Gate(users, store.NewPrincipals(pool))
}
