package model

// Actor identifies the authenticated caller. PrincipalID is nil for
// password-auth (the tier-all view); non-nil for device tokens.
type Actor struct {
	UserID      int64
	Username    string
	PrincipalID *int64
}
