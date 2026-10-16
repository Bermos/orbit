package server

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Protections for running Orbit on the open internet: client addresses that
// cannot be spoofed, browser security headers, rate limits on password and
// invite guessing, and outbound requests that cannot be pointed at the
// network Orbit runs in.

// realIP replaces the connection's address with the client's when Orbit runs
// behind a reverse proxy (TrustProxyHeaders). Without it the headers are
// ignored: anyone could send them to dodge rate limits.
func (s *Server) realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.TrustProxyHeaders {
			if ip := proxyClientIP(r.Header); ip.IsValid() {
				r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
			}
		}
		next.ServeHTTP(w, r)
	})
}

// proxyClientIP reads the address the proxy saw. X-Real-IP is set by the
// proxy itself; in X-Forwarded-For only the last entry was added by it, the
// ones before come from the client.
func proxyClientIP(h http.Header) netip.Addr {
	if ip, err := netip.ParseAddr(strings.TrimSpace(h.Get("X-Real-IP"))); err == nil {
		return ip.Unmap()
	}
	if xff := h.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(xff[len(xff)-1], ",")
		if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
			return ip.Unmap()
		}
	}
	return netip.Addr{}
}

// remoteIP is the client's address, for rate limiting.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// securityHeaders keeps the UI from being framed or fed foreign scripts.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	hsts := strings.HasPrefix(s.BaseURL, "https://")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; "+
			"base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// limiter counts events per key in a fixed window and blocks a key once it
// reaches max. It is in memory: limits are per Orbit replica and reset on
// restart, which is enough to make guessing passwords impractical.
type limiter struct {
	max    int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	start time.Time
	n     int
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, buckets: map[string]*bucket{}, now: time.Now}
}

// retryAfter returns how long key is blocked for, or 0.
func (l *limiter) retryAfter(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil || b.n < l.max {
		return 0
	}
	left := b.start.Add(l.window).Sub(l.now())
	if left <= 0 {
		delete(l.buckets, key)
		return 0
	}
	return left
}

// add records an event for key.
func (l *limiter) add(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) > 10000 {
		for k, b := range l.buckets {
			if now.Sub(b.start) >= l.window {
				delete(l.buckets, k)
			}
		}
	}
	b := l.buckets[key]
	if b == nil || now.Sub(b.start) >= l.window {
		b = &bucket{start: now}
		l.buckets[key] = b
	}
	b.n++
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// limits are the rate limits on guessing credentials.
type limits struct {
	// Failed sign-ins (web, API and git) per client address and per
	// username. The per-user limit is lower; it slows down guessing one
	// account's password from many addresses.
	authIP, authUser *limiter
	// Attempts to create an account, per client address.
	register *limiter
}

func newLimits() limits {
	return limits{
		authIP:   newLimiter(30, 15*time.Minute),
		authUser: newLimiter(10, 15*time.Minute),
		register: newLimiter(10, time.Hour),
	}
}

// tooManyAttempts reports when the client is blocked by any of the keys.
func tooManyAttempts(w http.ResponseWriter, checks ...func() time.Duration) error {
	var wait time.Duration
	for _, c := range checks {
		wait = max(wait, c())
	}
	if wait == 0 {
		return nil
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	return errStatus(http.StatusTooManyRequests, "too many attempts; try again later")
}

// specialNetworks are not reachable on the public internet (or translate to
// addresses that might not be) beyond what netip's predicates cover.
var specialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT, also Tailscale
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), // 6to4
	netip.MustParsePrefix("fec0::/10"),
}

// isPublicIP reports whether ip is a global unicast address on the internet.
func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range specialNetworks {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// outboundAllowed reports whether Orbit may connect to ip on a user's behalf:
// public addresses, and networks the operator allowed explicitly.
func outboundAllowed(ip netip.Addr, allowed []netip.Prefix) bool {
	ip = ip.Unmap()
	for _, p := range allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return isPublicIP(ip)
}

// ParseNetworks parses a comma-separated list of CIDRs or addresses, such as
// ORBIT_WEBHOOK_ALLOWED_NETWORKS.
func ParseNetworks(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			ip, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("%q is not an address or CIDR", f)
			}
			out = append(out, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address or CIDR", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// dialControl runs after DNS resolution, right before connecting, so a name
// that resolves to an internal address (or is rebound to one) is refused too.
func (s *Server) dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !outboundAllowed(ip, s.WebhookAllowedNetworks) {
		return fmt.Errorf("%s is a private or local address; Orbit only connects there if ORBIT_WEBHOOK_ALLOWED_NETWORKS includes it", ip.Unmap())
	}
	return nil
}

// newOutboundClient is the HTTP client for requests to user-supplied URLs.
// It connects directly (no proxy from the environment), because the
// address check must see the real destination.
func (s *Server) newOutboundClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: s.dialControl}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        10,
			IdleConnTimeout:     90 * time.Second,
		},
		// A hook gets exactly the URL it was given.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
