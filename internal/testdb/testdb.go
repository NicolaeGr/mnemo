// Package testdb hands each test package its own throwaway Postgres database,
// so packages that reset their schema can run in parallel under `go test ./...`
// without stomping on each other (or on the dev DB).
package testdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

var (
	once  sync.Once
	dbURL string
	dbErr error
)

// URL returns the DSN for this test binary's isolated database (base db name
// plus suffix), creating it on first use. Only intended to be imported from
// _test.go files; each package passes one fixed suffix.
func URL(suffix string) (string, error) {
	once.Do(func() {
		// Read PGHOST instead of a fixed socket path; devenv sets it to its own
		// runtime directory. When unset, libpq uses the default socket.
		host := os.Getenv("PGHOST")
		port := env("PGPORT", "5432")
		user := env("PGUSER", os.Getenv("USER"))
		base := env("PGDATABASE", os.Getenv("USER"))
		dbURL, dbErr = ensure(host, port, user, base, base+"_"+suffix)
	})
	return dbURL, dbErr
}

func ensure(host, port, user, base, target string) (string, error) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn(host, port, user, base))
	if err != nil {
		return "", fmt.Errorf("testdb: connect to %q: %w", base, err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`, target,
	).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		// CREATE DATABASE can't run in a transaction; conn is in autocommit.
		if _, err := conn.Exec(ctx, `CREATE DATABASE "`+target+`"`); err != nil {
			return "", fmt.Errorf("testdb: create %q: %w", target, err)
		}
	}
	return dsn(host, port, user, target), nil
}

func dsn(host, port, user, db string) string {
	parts := make([]string, 0, 5)
	if host != "" {
		parts = append(parts, "host="+host)
	}
	parts = append(parts, "port="+port, "user="+user, "dbname="+db, "sslmode=disable")
	return strings.Join(parts, " ")
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
