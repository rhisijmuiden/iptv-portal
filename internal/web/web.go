// Package web holds what the admin panel and the client portal share: the
// stylesheet, login rate limiting, client address handling and the health
// check.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

// App is the name this program reports in /healthz and station.json.
const App = "iptv-portal"

// StationContract is the version of the Build My TV Station contract this
// program implements.
const StationContract = 1

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

// trustedProxies holds the configured reverse proxy addresses; nil (the
// default) keeps the built-in rules below.
var trustedProxies atomic.Pointer[[]*net.IPNet]

// SetTrustedProxies limits the X-Forwarded-* headers to requests from these
// networks. An empty list restores the default rules.
func SetTrustedProxies(nets []*net.IPNet) {
	if len(nets) == 0 {
		trustedProxies.Store(nil)
		return
	}
	trustedProxies.Store(&nets)
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host
}

func isTrusted(nets []*net.IPNet, addr string) bool {
	ip := net.ParseIP(strings.TrimSpace(addr))
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// fromTrustedProxy reports whether r came from a configured trusted proxy.
// ok is false when no proxies are configured.
func fromTrustedProxy(r *http.Request) (trusted, ok bool) {
	nets := trustedProxies.Load()
	if nets == nil {
		return false, false
	}
	return isTrusted(*nets, remoteIP(r)), true
}

// ClientIP returns the remote address. Without configured trusted proxies,
// X-Forwarded-For is only trusted when the request comes from a local or
// private address, i.e. a reverse proxy on the same host or network. With
// trusted proxies, it is only read from those, and the client is the last
// address in the chain that is not a trusted proxy itself.
func ClientIP(r *http.Request) string {
	host := remoteIP(r)
	fwd := r.Header.Get("X-Forwarded-For")
	if nets := trustedProxies.Load(); nets != nil {
		if fwd == "" || !isTrusted(*nets, host) {
			return host
		}
		parts := strings.Split(fwd, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			if p := strings.TrimSpace(parts[i]); p != "" && !isTrusted(*nets, p) {
				return p
			}
		}
		return strings.TrimSpace(parts[0])
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) && fwd != "" {
		parts := strings.Split(fwd, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return host
}

// IsHTTPS reports whether the client used HTTPS, directly or via a proxy.
// With trusted proxies configured, X-Forwarded-Proto is only believed from
// those.
func IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if trusted, ok := fromTrustedProxy(r); ok && !trusted {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// requestHost is the host the client asked for. X-Forwarded-Host is only
// used when it comes from a configured trusted proxy.
func requestHost(r *http.Request) string {
	if trusted, _ := fromTrustedProxy(r); trusted {
		if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" {
			return fh
		}
	}
	return r.Host
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
	return scheme + "://" + requestHost(r)
}

// Health answers /healthz with the status, name, version and station
// contract of this program. It needs no login and holds no secrets.
func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		Status   string `json:"status"`
		App      string `json:"app"`
		Version  string `json:"version"`
		Contract int    `json:"contract"`
	}{"ok", App, version.Version, StationContract})
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
