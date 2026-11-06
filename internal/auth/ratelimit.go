package auth

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Limiter throttles credential failures for one plane. A success clears the
// caller, so a working client never accumulates toward the limit.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string]*failWindow
}

type failWindow struct {
	count int
	reset time.Time
}

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, failures: make(map[string]*failWindow)}
}

// Allow reports whether the key is under the limit at now.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.failures[key]
	return b == nil || now.After(b.reset) || b.count < l.max
}

// Fail records one failure, starting a fresh window when the old one expired.
// Past maxTrackedKeys it deletes expired entries first, so many distinct
// addresses cannot grow the map without bound.
func (l *Limiter) Fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.failures) > maxTrackedKeys {
		for k, w := range l.failures {
			if now.After(w.reset) {
				delete(l.failures, k)
			}
		}
	}
	b := l.failures[key]
	if b == nil || now.After(b.reset) {
		b = &failWindow{reset: now.Add(l.window)}
		l.failures[key] = b
	}
	b.count++
}

// Clear forgets the key, so a successful login starts from zero.
func (l *Limiter) Clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

const maxTrackedKeys = 10000

// ClientIPs resolves the request origin. X-Forwarded-For counts only when the
// direct peer is a configured proxy, so a client cannot pick its own address.
type ClientIPs struct {
	trusted []netip.Prefix
}

func NewClientIPs(trusted []string) (*ClientIPs, error) {
	if len(trusted) == 0 {
		return DefaultClientIPs(), nil
	}
	prefixes := make([]netip.Prefix, 0, len(trusted))
	for _, s := range trusted {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, p.Masked())
	}
	return &ClientIPs{trusted: prefixes}, nil
}

var loopbackIPs = &ClientIPs{trusted: []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
}}

// DefaultClientIPs trusts loopback, which is where a same-host reverse proxy
// connects from.
func DefaultClientIPs() *ClientIPs { return loopbackIPs }

// From returns the client address. Without a trusted peer it is the connection
// address. Otherwise it is the rightmost X-Forwarded-For entry that is not
// itself a trusted proxy. A chain with any unparseable entry falls back to the
// peer address, so injected garbage cannot move the index.
func (c *ClientIPs) From(r *http.Request) string {
	peer, ok := parseAddr(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if c == nil || !c.trusts(peer) {
		return peer.String()
	}
	chain, ok := forwardedChain(r)
	if !ok {
		return peer.String()
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !c.trusts(chain[i]) {
			return chain[i].String()
		}
	}
	return peer.String()
}

func (c *ClientIPs) trusts(ip netip.Addr) bool {
	for _, p := range c.trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func forwardedChain(r *http.Request) ([]netip.Addr, bool) {
	var out []netip.Addr
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(header, ",") {
			addr, ok := parseAddr(strings.TrimSpace(part))
			if !ok {
				return nil, false
			}
			out = append(out, addr)
		}
	}
	return out, len(out) > 0
}

func parseAddr(s string) (netip.Addr, bool) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

func tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
}
