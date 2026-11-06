package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/nicolaegr/mnemo/internal/model"
	"github.com/nicolaegr/mnemo/internal/store"
)

// Actor aliases model.Actor so auth's API keeps reading auth.Actor at call
// sites while store can depend on model without an import cycle.
type Actor = model.Actor

type actorKey struct{}

// WithActor stores the resolved actor in the request context.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor resolved by the auth middleware.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}

// Config is the shared credential policy. Each plane builds its own gate from
// it, so a device hammering DAV cannot lock the web UI out.
type Config struct {
	// IPs resolves the client address behind a reverse proxy. Nil trusts
	// loopback only.
	IPs *ClientIPs
	// FailuresPerMinute caps credential failures per client address on each
	// plane. Zero means defaultFailuresPerMinute.
	FailuresPerMinute int
	// TokenPassword lets a DAV client send its device token as the Basic
	// password, for clients that cannot send a Bearer header. DAV only. Off by
	// default, because clients store Basic passwords as long-lived account
	// secrets.
	TokenPassword bool
}

const defaultFailuresPerMinute = 20

// MinPasswordLength is the floor for account passwords on signup, bootstrap,
// and the set-password flag.
const MinPasswordLength = 8

// Gate authenticates one plane.
type Gate struct {
	users         *store.Users
	principals    *store.PrincipalStore
	ips           *ClientIPs
	failures      *Limiter
	tokenPassword bool
}

// Gate builds an authenticator for one plane. Each call gets its own failure
// limiter.
func (c Config) Gate(users *store.Users, principals *store.PrincipalStore) *Gate {
	return &Gate{
		users:         users,
		principals:    principals,
		ips:           c.ips(),
		failures:      NewLimiter(c.failuresPerMinute(), time.Minute),
		tokenPassword: c.TokenPassword,
	}
}

// Guard returns a limiter and IP resolver for a caller that verifies requests
// itself, such as the admin login form or the signup endpoint. A max below one
// uses the plane default.
func (c Config) Guard(max int, window time.Duration) (*Limiter, *ClientIPs) {
	if max <= 0 {
		max = c.failuresPerMinute()
	}
	return NewLimiter(max, window), c.ips()
}

func (c Config) ips() *ClientIPs {
	if c.IPs == nil {
		return DefaultClientIPs()
	}
	return c.IPs
}

func (c Config) failuresPerMinute() int {
	if c.FailuresPerMinute <= 0 {
		return defaultFailuresPerMinute
	}
	return c.FailuresPerMinute
}

type policy struct {
	bearer        bool
	tokenPassword bool
}

// DAV admits a device principal (Bearer, or a token as the Basic password when
// enabled) or an account (Basic login and password). An account has no
// principal and sees every tier; a principal's tier gates it.
func (g *Gate) DAV(next http.Handler) http.Handler {
	return g.require(next, policy{bearer: true, tokenPassword: g.tokenPassword})
}

// Account admits Basic account credentials only.
func (g *Gate) Account(next http.Handler) http.Handler {
	return g.require(next, policy{})
}

func (g *Gate) require(next http.Handler, p policy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := g.ips.From(r)
		now := time.Now()
		if !g.failures.Allow(ip, now) {
			tooManyRequests(w)
			return
		}
		actor, ok := g.resolve(r, p)
		if !ok {
			g.failures.Fail(ip, now)
			unauthorized(w)
			return
		}
		g.failures.Clear(ip)
		next.ServeHTTP(w, r.WithContext(WithActor(r.Context(), actor)))
	})
}

func (g *Gate) resolve(r *http.Request, p policy) (Actor, bool) {
	if p.bearer {
		if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
			return g.principal(r.Context(), strings.TrimPrefix(authz, "Bearer "))
		}
	}
	login, password, ok := r.BasicAuth()
	if !ok {
		return Actor{}, false
	}
	if u, ok := g.users.Authenticate(r.Context(), login, password); ok {
		return Actor{UserID: u.ID, Username: u.Username}, true
	}
	// Token presented as the password, tried only after the account check and
	// only when enabled.
	if p.tokenPassword {
		return g.principal(r.Context(), password)
	}
	return Actor{}, false
}

func (g *Gate) principal(ctx context.Context, raw string) (Actor, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Actor{}, false
	}
	p, err := g.principals.ByTokenHash(ctx, store.TokenHashOf(raw))
	if err != nil {
		return Actor{}, false
	}
	u, err := g.users.ByID(ctx, p.UserID)
	if err != nil {
		return Actor{}, false
	}
	_ = g.principals.Touch(ctx, p.ID)
	id := p.ID
	return Actor{UserID: p.UserID, Username: u.Username, PrincipalID: &id}, true
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="contacts", charset="UTF-8"`)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}
