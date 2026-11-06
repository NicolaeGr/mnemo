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
	// password, for clients that cannot send a Bearer header. It affects the DAV
	// plane only. Off by default, because clients store Basic passwords as
	// long-lived account secrets.
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

// tokenPasswordEnabled reads DAV_TOKEN_PASSWORD, falling back to the old
// INSECURE_TOKEN_AUTH name so existing deployments keep working.
func tokenPasswordEnabled() bool {
	if v := os.Getenv("DAV_TOKEN_PASSWORD"); v != "" {
		return v == "true"
	}
	return os.Getenv("INSECURE_TOKEN_AUTH") == "true"
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
