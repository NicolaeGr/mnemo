package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// failLimiter throttles auth-failure bursts per client IP. A success clears the
// IP, so a working client never accumulates toward the limit.
type failLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string]*failWindow
}

type failWindow struct {
	count int
	reset time.Time
}

func newFailLimiter(max int, window time.Duration) *failLimiter {
	return &failLimiter{max: max, window: window, failures: make(map[string]*failWindow)}
}

// allowed reports whether the IP is under the limit at now.
func (l *failLimiter) allowed(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.failures[ip]
	return b == nil || now.After(b.reset) || b.count < l.max
}

// fail records one failure, starting a fresh window when the old one expired.
func (l *failLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.failures[ip]
	if b == nil || now.After(b.reset) {
		b = &failWindow{reset: now.Add(l.window)}
		l.failures[ip] = b
	}
	b.count++
}

func (l *failLimiter) clear(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

// clientIP is the request peer address without its port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
}
