package auth

import (
	"net"
	"net/http"
	"strings"
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
// When the map grows past maxTrackedIPs, it deletes expired entries first so
// many distinct IPs cannot grow it without bound.
func (l *failLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) > maxTrackedIPs {
		for k, w := range l.failures {
			if now.After(w.reset) {
				delete(l.failures, k)
			}
		}
	}
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

// maxTrackedIPs caps the limiter's memory use. Past it, every failure deletes
// expired entries.
const maxTrackedIPs = 10000

// clientIP is the request's origin. Behind a trusted proxy that is the leftmost
// X-Forwarded-For entry, otherwise the peer address without its port. Without
// this, one shared proxy address would throttle every caller as the same IP.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// These expose the shared per-IP auth-failure limiter so callers outside the DAV
// handler, such as the admin login form, throttle credential guessing the same
// way RequireDAV does.
func ClientIP(r *http.Request) string                { return clientIP(r) }
func AllowAuthAttempt(ip string, now time.Time) bool { return authFailures.allowed(ip, now) }
func NoteAuthFailure(ip string, now time.Time)       { authFailures.fail(ip, now) }
func ClearAuthFailures(ip string)                    { authFailures.clear(ip) }

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
}
