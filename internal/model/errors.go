package model

import "errors"

var (
	ErrNotFound        = errors.New("not found")
	ErrPrecondition    = errors.New("precondition failed")
	ErrConflict        = errors.New("conflict")
	ErrUIDConflict     = errors.New("live UID conflict")
	ErrFilenameRetired = errors.New("filename recently deleted")
	ErrUsernameTaken   = errors.New("username taken")
	ErrEmailTaken      = errors.New("email taken")
	ErrInvalidTiers    = errors.New("a book needs at least one tier, and archived cannot be combined with primary or secondary")
)
