package auth

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	now := time.Now()
	key := "203.0.113.7"

	for i := 0; i < 3; i++ {
		if !l.Allow(key, now) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		l.Fail(key, now)
	}
	if l.Allow(key, now) {
		t.Fatal("key should be blocked after reaching the limit")
	}

	l.Clear(key)
	if !l.Allow(key, now) {
		t.Fatal("key should be allowed again after clear")
	}

	l.Fail(key, now)
	l.Fail(key, now)
	l.Fail(key, now)
	if l.Allow(key, now) {
		t.Fatal("key should still be blocked within the window")
	}
	if !l.Allow(key, now.Add(2*time.Minute)) {
		t.Fatal("key should be allowed after the window passes")
	}

	if !l.Allow("198.51.100.1", now) {
		t.Fatal("a different key should not be limited")
	}
}

func TestClientIPsIgnoresForwardedForFromUntrustedPeer(t *testing.T) {
	ips, err := NewClientIPs([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("new client ips: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	if got := ips.From(r); got != "203.0.113.9" {
		t.Fatalf("from = %q, want peer address", got)
	}
}

func TestClientIPsReadsRightmostUntrustedForwardedEntry(t *testing.T) {
	ips, err := NewClientIPs([]string{"10.0.0.0/8", "127.0.0.1/32"})
	if err != nil {
		t.Fatalf("new client ips: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 198.51.100.7, 10.0.0.5")

	if got := ips.From(r); got != "198.51.100.7" {
		t.Fatalf("from = %q, want 198.51.100.7", got)
	}
}

func TestClientIPsFallsBackOnUnparseableChain(t *testing.T) {
	ips := DefaultClientIPs()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, not-an-ip")

	if got := ips.From(r); got != "127.0.0.1" {
		t.Fatalf("from = %q, want the peer address", got)
	}
}

func TestClientIPsDefaultsToLoopbackTrust(t *testing.T) {
	ips, err := NewClientIPs(nil)
	if err != nil {
		t.Fatalf("new client ips: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")

	if got := ips.From(r); got != "198.51.100.7" {
		t.Fatalf("from = %q, want the forwarded client", got)
	}

	direct := httptest.NewRequest("GET", "/", nil)
	direct.RemoteAddr = "203.0.113.9:5555"
	direct.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := ips.From(direct); got != "203.0.113.9" {
		t.Fatalf("from = %q, want the peer address", got)
	}
}
