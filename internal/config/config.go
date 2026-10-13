package config

import (
	"os"
	"time"
)

type Config struct {
	Addr string

	DatabaseURL string

	SessionName    string
	SessionTTL     time.Duration
	SessionSecret  string
	CookieSecure   bool
	CookieSameSite string

	BcryptCost int

	// InsecureTokenAuth lets a device send its token as the Basic password, for
	// CardDAV clients that cannot send a Bearer header. Off by default.
	InsecureTokenAuth bool
}

func Load() Config {
	return Config{
		Addr:              getenv("ADDR", ":8080"),
		DatabaseURL:       databaseURL(),
		SessionName:       getenv("SESSION_NAME", "session"),
		SessionTTL:        72 * time.Hour,
		SessionSecret:     os.Getenv("SESSION_SECRET"),
		CookieSecure:      getenv("COOKIE_SECURE", "false") == "true",
		CookieSameSite:    getenv("COOKIE_SAMESITE", "lax"),
		BcryptCost:        12,
		InsecureTokenAuth: getenv("INSECURE_TOKEN_AUTH", "false") == "true",
	}
}

func databaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	host := getenv("PGHOST", "localhost")
	port := getenv("PGPORT", "5432")
	user := getenv("PGUSER", os.Getenv("USER"))
	db := getenv("PGDATABASE", os.Getenv("USER"))
	return "host=" + host + " port=" + port + " user=" + user + " dbname=" + db + " sslmode=disable"
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
