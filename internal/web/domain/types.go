// Package domain holds the data a segment's Load returns; the layout's Render
// casts it back.
package domain

// User is loaded by the "dashboard" segment when it mounts.
type User struct {
	Name  string
	Email string
}
