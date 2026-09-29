package auth

import (
	"testing"
	"time"
)

func TestFailLimiter(t *testing.T) {
	l := newFailLimiter(3, time.Minute)
	now := time.Now()
	ip := "203.0.113.7"

	for i := 0; i < 3; i++ {
		if !l.allowed(ip, now) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.fail(ip, now)
	}
	if l.allowed(ip, now) {
		t.Fatal("IP should be blocked after reaching the limit")
	}

	// A success forgets the IP entirely.
	l.clear(ip)
	if !l.allowed(ip, now) {
		t.Fatal("IP should be allowed again after clear")
	}

	// The window rolls over and the counter resets.
	l.fail(ip, now)
	l.fail(ip, now)
	l.fail(ip, now)
	if l.allowed(ip, now) {
		t.Fatal("IP should still be blocked within the window")
	}
	if !l.allowed(ip, now.Add(2*time.Minute)) {
		t.Fatal("IP should be allowed after the window passes")
	}

	// Other IPs are unaffected.
	if !l.allowed("198.51.100.1", now) {
		t.Fatal("a different IP should not be limited")
	}
}
