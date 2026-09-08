package model

// Actor identifies the authenticated caller. PrincipalID is nil for
// password-auth (the "tier-all" view per §4.1); non-nil for device tokens.
type Actor struct {
	UserID      int64
	Username    string
	PrincipalID *int64
}
