package config

import (
	"os"
	"strconv"
	"strings"
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

	// TrustedProxies lists the CIDRs whose X-Forwarded-For header is honoured.
	// Empty trusts loopback only.
	TrustedProxies []string

	// AuthFailureLimit caps credential failures per client address per minute on
	// each plane.
	AuthFailureLimit int

	// DAVTokenPassword lets a DAV client send its device token as the Basic
	// password, for clients that cannot send a Bearer header. On by default,
	// since DAVx5 and Thunderbird only speak Basic and would otherwise be stuck
	// to the account password and every tier. Set DAV_TOKEN_PASSWORD=false to
	// disable it.
	DAVTokenPassword bool
}

func Load() Config {
	return Config{
		Addr:             getenv("ADDR", ":8080"),
		DatabaseURL:      databaseURL(),
		SessionName:      getenv("SESSION_NAME", "session"),
		SessionTTL:       72 * time.Hour,
		SessionSecret:    os.Getenv("SESSION_SECRET"),
		CookieSecure:     getenv("COOKIE_SECURE", "false") == "true",
		CookieSameSite:   getenv("COOKIE_SAMESITE", "lax"),
		BcryptCost:       12,
		TrustedProxies:   splitList(os.Getenv("TRUSTED_PROXIES")),
		AuthFailureLimit: getenvInt("AUTH_FAILURE_LIMIT", 20),
		DAVTokenPassword: tokenPasswordEnabled(),
	}
}

// tokenPasswordEnabled reports whether a device token may be sent as the Basic
// password. It defaults to true, because DAVx5 and Thunderbird cannot send a
// Bearer header. Set DAV_TOKEN_PASSWORD=false to reject tokens in that slot.
// INSECURE_TOKEN_AUTH is the old name and is still honoured.
func tokenPasswordEnabled() bool {
	if v, ok := os.LookupEnv("DAV_TOKEN_PASSWORD"); ok {
		return v != "false"
	}
	if v, ok := os.LookupEnv("INSECURE_TOKEN_AUTH"); ok {
		return v == "true"
	}
	return true
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

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
