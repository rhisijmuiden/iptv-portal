// Package server runs the two HTTP listeners (admin panel and client portal)
// and restarts them when their port changes.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// listener is one restartable HTTP server.
type listener struct {
	name    string
	handler http.Handler
	port    int
	srv     *http.Server
	err     error // last start error, shown in the admin panel
}

// Manager owns the admin and portal listeners.
type Manager struct {
	mu     sync.Mutex
	admin  *listener
	portal *listener
	log    *slog.Logger
}

// NewManager creates a manager; call Start to begin listening.
func NewManager(log *slog.Logger, admin, portal http.Handler) *Manager {
	return &Manager{
		admin:  &listener{name: "beheerpaneel", handler: admin},
		portal: &listener{name: "portal", handler: portal},
		log:    log,
	}
}

// Start (re)starts any listener whose port differs from the running one.
// A listener that fails to start is logged and reported by Status; the
// other listener keeps running.
func (m *Manager) Start(adminPort, portalPort int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apply(m.admin, adminPort)
	m.apply(m.portal, portalPort)
}

func (m *Manager) apply(l *listener, port int) {
	if l.srv != nil && l.port == port {
		return
	}
	if l.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		l.srv.Shutdown(ctx)
		cancel()
		l.srv = nil
	}
	l.port = port
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		l.err = err
		m.log.Error("kan niet luisteren", "listener", l.name, "port", port, "err", err)
		return
	}
	l.err = nil
	l.srv = &http.Server{
		Handler:           l.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	srv := l.srv
	m.log.Info("luistert", "listener", l.name, "port", port)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("listener gestopt", "listener", l.name, "err", err)
		}
	}()
}

// Status describes a listener for the admin panel.
type Status struct {
	Name    string
	Port    int
	Running bool
	Error   string
}

// Status returns the state of both listeners.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	for _, l := range []*listener{m.admin, m.portal} {
		s := Status{Name: l.name, Port: l.port, Running: l.srv != nil}
		if l.err != nil {
			s.Error = l.err.Error()
		}
		out = append(out, s)
	}
	return out
}

// PortAvailable reports whether port can be used: it is either already used
// by one of our own listeners, or free to bind.
func (m *Manager) PortAvailable(port int) error {
	m.mu.Lock()
	for _, l := range []*listener{m.admin, m.portal} {
		if l.srv != nil && l.port == port {
			m.mu.Unlock()
			return nil
		}
	}
	m.mu.Unlock()
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("poort %d is niet beschikbaar: %w", port, err)
	}
	return ln.Close()
}

// Shutdown stops both listeners.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range []*listener{m.admin, m.portal} {
		if l.srv != nil {
			l.srv.Shutdown(ctx)
			l.srv = nil
		}
	}
}
