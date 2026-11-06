package config

import (
	"os"
	"testing"
)

func TestTokenPasswordEnabled(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		dav    *string
		legacy *string
		want   bool
	}{
		{"default", nil, nil, true},
		{"explicit true", str("true"), nil, true},
		{"explicit false", str("false"), nil, false},
		{"legacy true", nil, str("true"), true},
		{"legacy false", nil, str("false"), false},
		{"dav wins over legacy", str("false"), str("true"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withEnv(t, "DAV_TOKEN_PASSWORD", c.dav)
			withEnv(t, "INSECURE_TOKEN_AUTH", c.legacy)
			if got := tokenPasswordEnabled(); got != c.want {
				t.Fatalf("tokenPasswordEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

func withEnv(t *testing.T, key string, value *string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if value == nil {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, *value)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
			return
		}
		os.Unsetenv(key)
	})
}
