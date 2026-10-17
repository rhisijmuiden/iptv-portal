// Package portal serves the client side: the address users enter in their
// IPTV player (Xtream Codes API, M3U playlist, XMLTV guide) and the
// invitation pages where they create an account.
package portal

import (
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

//go:embed templates/*.html
var templateFS embed.FS

// Handler is the client portal HTTP handler.
type Handler struct {
	store   *store.Store
	log     *slog.Logger
	mux     *http.ServeMux
	pages   map[string]*template.Template
	limiter *web.Limiter
	// authLimiter slows down password guessing on the player endpoints.
	authLimiter *web.Limiter
	guide       *epg.Guide
	activity    *activity.Tracker
}

// New builds the client portal. Player requests are recorded in tracker.
func New(st *store.Store, guide *epg.Guide, tracker *activity.Tracker, log *slog.Logger) (*Handler, error) {
	h := &Handler{store: st, log: log, mux: http.NewServeMux(), guide: guide, activity: tracker,
		limiter: web.NewLimiter(20, 15*time.Minute), authLimiter: web.NewLimiter(30, 15*time.Minute)}
	h.pages = map[string]*template.Template{}
	for _, name := range []string{"invite", "welcome"} {
		t, err := template.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		h.pages[name] = t
	}
	h.mux.Handle("GET /static/", web.Static())
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	h.mux.HandleFunc("GET /{$}", h.index)
	h.mux.HandleFunc("GET /invite/{token}", h.inviteForm)
	h.mux.HandleFunc("POST /invite/{token}", h.inviteRedeem)
	h.mux.HandleFunc("/player_api.php", h.playerAPI)
	h.mux.HandleFunc("/get.php", h.getPlaylist)
	h.mux.HandleFunc("/xmltv.php", h.xmltv)
	h.mux.HandleFunc("GET /live/{user}/{pass}/{file}", h.liveRedirect)
	return h, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) serverError(w http.ResponseWriter, err error) {
	h.log.Error("internal error", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(st.PortalName + "\n"))
}

// ---- invitations ----

type invitePage struct {
	Title      string
	PortalName string
	Error      string
	Usable     bool
	Username   string
	Base       string
	ExpiresAt  string
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, status int, p invitePage) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	p.PortalName = st.PortalName
	p.Base = web.BaseURL(st.PublicURL, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer") // keep the invite token out of referrers
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if err := h.pages[name].ExecuteTemplate(w, "layout.html", p); err != nil {
		h.log.Error("rendering template", "page", name, "err", err)
	}
}

func (h *Handler) inviteForm(w http.ResponseWriter, r *http.Request) {
	inv, err := h.store.InviteByToken(r.Context(), r.PathValue("token"))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	status := http.StatusOK
	if err != nil || !inv.Usable() {
		status = http.StatusNotFound
	}
	h.render(w, r, "invite", status, invitePage{Title: "Invitation", Usable: status == http.StatusOK})
}

func (h *Handler) inviteRedeem(w http.ResponseWriter, r *http.Request) {
	ip := web.ClientIP(r)
	p := invitePage{Title: "Invitation", Usable: true, Username: r.FormValue("username")}
	if !h.limiter.Allow(ip) {
		p.Error = "Too many attempts, try again in 15 minutes."
		h.render(w, r, "invite", http.StatusTooManyRequests, p)
		return
	}
	if r.FormValue("password") != r.FormValue("confirm") {
		p.Error = "The passwords do not match."
		h.render(w, r, "invite", http.StatusBadRequest, p)
		return
	}
	u, err := h.store.RedeemInvite(r.Context(), r.PathValue("token"), r.FormValue("username"), r.FormValue("password"))
	if errors.Is(err, store.ErrInviteUnusable) {
		h.limiter.Fail(ip) // slows down guessing of tokens
		h.render(w, r, "invite", http.StatusNotFound, invitePage{Title: "Invitation"})
		return
	}
	if err != nil {
		h.limiter.Fail(ip)
		p.Error = web.Sentence(err.Error())
		h.render(w, r, "invite", http.StatusBadRequest, p)
		return
	}
	h.log.Info("account created from invitation", "username", u.Username, "ip", ip)
	page := invitePage{Title: "Welcome", Username: u.Username}
	if u.ExpiresAt != nil {
		page.ExpiresAt = u.ExpiresAt.Local().Format("2 January 2006")
	}
	h.render(w, r, "welcome", http.StatusOK, page)
}
