// Package uictx carries the signed-in session into template rendering: the
// account identity and the CSRF token forms must echo back.
package uictx

import "context"

type Session struct {
	UserID   int64
	Username string
	Name     string
	Email    string
	CSRF     string
}

type ctxKey struct{}

func With(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

func From(ctx context.Context) Session {
	s, _ := ctx.Value(ctxKey{}).(Session)
	return s
}
