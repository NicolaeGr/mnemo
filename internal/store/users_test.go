package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

func TestAuthenticate(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, err := users.Signup(ctx, "alice", "Alice@Example.com", "Alice", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}

	cases := []struct {
		name  string
		login string
		pass  string
		want  bool
	}{
		{"username", "alice", "secret", true},
		{"email as stored", "Alice@Example.com", "secret", true},
		{"email lowercase", "alice@example.com", "secret", true},
		{"wrong password", "alice", "nope", false},
		{"unknown login", "bob", "secret", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, ok := users.Authenticate(ctx, c.login, c.pass)
			if ok != c.want {
				t.Fatalf("Authenticate(%q) ok = %v, want %v", c.login, ok, c.want)
			}
			if ok && u.Username != "alice" {
				t.Fatalf("user = %+v, want alice", u)
			}
		})
	}
}

func TestByLoginRejectsWildcards(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, err := users.Signup(ctx, "alice", "alice@example.com", "Alice", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	if _, err := users.ByLogin(ctx, "%"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("ByLogin(wildcard) err = %v, want ErrNotFound", err)
	}
}

func TestSetPassword(t *testing.T) {
	pool := resetPool(t)
	ctx := context.Background()
	users := store.NewUsers(pool)
	if _, err := users.Signup(ctx, "alice", "alice@example.com", "Alice", "secret", 4); err != nil {
		t.Fatalf("signup: %v", err)
	}
	if err := users.SetPassword(ctx, "alice", "newsecret", 4); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, ok := users.Authenticate(ctx, "alice", "secret"); ok {
		t.Fatal("old password still works")
	}
	if _, ok := users.Authenticate(ctx, "alice", "newsecret"); !ok {
		t.Fatal("new password does not work")
	}
	if err := users.SetPassword(ctx, "bob", "x", 4); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("unknown login err = %v, want ErrNotFound", err)
	}
}
