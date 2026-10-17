// Package portal serves the client side: the address users enter in their
// IPTV player. The M3U playlist, Xtream Codes API and EPG are added here.
package portal

import (
	"log/slog"
	"net/http"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Handler is the client portal HTTP handler.
type Handler struct {
	store *store.Store
	log   *slog.Logger
	mux   *http.ServeMux
}

// New builds the client portal.
func New(st *store.Store, log *slog.Logger) *Handler {
	h := &Handler{store: st, log: log, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	h.mux.HandleFunc("GET /{$}", h.index)
	return h
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.log.Error("instellingen lezen", "err", err)
		http.Error(w, "interne fout", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(st.PortalName + "\n"))
}
