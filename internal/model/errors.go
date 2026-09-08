package model

import "errors"

var (
	ErrNotFound        = errors.New("not found")
	ErrPrecondition    = errors.New("precondition failed")
	ErrConflict        = errors.New("conflict")
	ErrUIDConflict     = errors.New("live UID conflict")
	ErrFilenameRetired = errors.New("filename recently deleted")
	ErrUnparsableVCard = errors.New("unparsable vcard")
	ErrUsernameTaken   = errors.New("username taken")
	ErrEmailTaken      = errors.New("email taken")
)
