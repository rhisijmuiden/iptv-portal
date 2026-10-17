// Package web holds what the admin panel and the client portal share: the
// stylesheet, login rate limiting and client address handling.
package web

import (
	"embed"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed static
var staticFS embed.FS

// Static serves the shared assets under /static/.
func Static() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}

// Limiter counts failed attempts per client address within a time window.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

// NewLimiter allows max failures per address within window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, failures: map[string][]time.Time{}}
}

func (l *Limiter) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-l.window)
	var keep []time.Time
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = keep
	}
	return keep
}

// Allow reports whether ip may make another attempt.
func (l *Limiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) < l.max
}

// Fail records a failed attempt.
func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.recent(ip), time.Now())
}

// Reset clears the failures of ip after a successful attempt.
func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

// ClientIP returns the remote address. X-Forwarded-For is only trusted when
// the request comes from a local or private address, i.e. a reverse proxy
// on the same host or network.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	return host
}

// IsHTTPS reports whether the client used HTTPS, directly or via a proxy.
func IsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// BaseURL returns the address clients use to reach the server that
// received r: publicURL when set, otherwise the scheme and host of r.
func BaseURL(publicURL string, r *http.Request) string {
	if publicURL != "" {
		return strings.TrimRight(publicURL, "/")
	}
	scheme := "http"
	if IsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// Sentence capitalises an error message and ends it with a period, for
// display on a page.
func Sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}
