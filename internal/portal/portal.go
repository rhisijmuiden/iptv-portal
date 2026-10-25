// Package portal serves the client side: the address users enter in their
// IPTV player (Xtream Codes API, M3U playlist, XMLTV guide), the
// invitation pages where they create an account and the account pages
// where they look after it.
package portal

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/logos"
	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/nowplaying"
	"github.com/rhisijmuiden/iptv-portal/internal/stats"
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
	pages   map[string]map[string]*template.Template // pages[lang][name]
	limiter *web.Limiter
	// authLimiter slows down password guessing on the player endpoints.
	authLimiter *web.Limiter
	guide       *epg.Guide
	activity    *activity.Tracker
	// vod serves On Demand; nil turns it off.
	vod *media.Catalog
	// nowPlaying reads what radio stations play; logos serves their logos.
	nowPlaying *nowplaying.Service
	logos      *logos.Cache
	// stats records views for the viewing statistics; nil records none.
	stats *stats.Recorder
}

// SetStats makes the portal record views in rec.
func (h *Handler) SetStats(rec *stats.Recorder) { h.stats = rec }

// New builds the client portal. Player requests are recorded in tracker.
func New(st *store.Store, guide *epg.Guide, tracker *activity.Tracker, vod *media.Catalog, log *slog.Logger) (*Handler, error) {
	h := &Handler{store: st, log: log, mux: http.NewServeMux(), guide: guide, activity: tracker, vod: vod,
		nowPlaying: nowplaying.New(), logos: logos.New(),
		limiter: web.NewLimiter(20, 15*time.Minute), authLimiter: web.NewLimiter(30, 15*time.Minute)}
	if err := h.parseTemplates(); err != nil {
		return nil, err
	}
	h.mux.Handle("GET /static/", web.Static())
	h.mux.HandleFunc("GET /healthz", web.Health)
	h.mux.HandleFunc("GET /{$}", h.index)
	h.mux.HandleFunc("GET /language", h.setLanguage)
	h.mux.HandleFunc("GET /invite/{token}", h.inviteForm)
	h.mux.HandleFunc("POST /invite/{token}", h.inviteRedeem)
	h.mux.HandleFunc("/player_api.php", h.playerAPI)
	h.mux.HandleFunc("/get.php", h.getPlaylist)
	h.mux.HandleFunc("/xmltv.php", h.xmltv)
	h.mux.HandleFunc("GET /live/{user}/{pass}/{file}", h.liveRedirect)
	h.mux.HandleFunc("GET /movie/{user}/{pass}/{file}", h.vodPlay(media.Movie))
	h.mux.HandleFunc("GET /series/{user}/{pass}/{file}", h.vodPlay(media.Episode))
	h.mux.HandleFunc("GET /vod-image/{id}/{kind}", h.vodImage)
	h.mux.HandleFunc("GET /logo/{file}", h.logo)
	h.accountRoutes()
	return h, nil
}

// parseTemplates parses every page once per language. The pages of a
// logged-in user get the layout with the menu (app.html), the others the
// plain layout. app.html defines "layout.html" itself, so every page is
// rendered by executing "layout.html".
func (h *Handler) parseTemplates() error {
	plain := []string{"invite", "welcome", "login", "forgot", "reset"}
	app := []string{"account", "setup", "history", "profile"}
	h.pages = map[string]map[string]*template.Template{}
	for _, l := range i18n.Languages {
		funcs := templateFuncs(l.Code)
		h.pages[l.Code] = map[string]*template.Template{}
		for _, name := range append(plain, app...) {
			layout := "layout.html"
			if slices.Contains(app, name) {
				layout = "app.html"
			}
			t, err := template.New("layout.html").Funcs(funcs).
				ParseFS(templateFS, "templates/"+layout, "templates/"+name+".html")
			if err != nil {
				return err
			}
			h.pages[l.Code][name] = t
		}
	}
	return nil
}

// templateFuncs are the template functions for lang.
func templateFuncs(lang string) template.FuncMap {
	funcs := template.FuncMap{
		// icon draws a symbol from /static/icons.svg.
		"icon": func(name string) template.HTML {
			return template.HTML(`<svg class="icon" aria-hidden="true"><use href="/static/icons.svg#` +
				template.HTMLEscapeString(name) + `"/></svg>`)
		},
		"datetime": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
		// duration shows a viewing time, e.g. "3 h 20 min".
		"duration": func(d time.Duration) string {
			m := int(d / time.Minute)
			switch {
			case m < 1:
				return i18n.T(lang, "under a minute")
			case m < 60:
				return i18n.Tf(lang, "%d min", m)
			case m < 100*60:
				return i18n.Tf(lang, "%d h %d min", m/60, m%60)
			}
			return i18n.Tf(lang, "%d h", m/60)
		},
	}
	for k, v := range i18n.Funcs(lang) {
		funcs[k] = v
	}
	return funcs
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
	// A browser goes to the account pages; players and scripts get the
	// name of the portal as before.
	if st.AccountPortal && strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(st.PortalName + "\n"))
}

// language is the language the visitor picked, or the portal's default.
func (h *Handler) language(r *http.Request, st store.Settings) string {
	return i18n.FromRequest(r, st.Language)
}

// setLanguage stores the language picked on an invitation page and goes
// back to that page.
func (h *Handler) setLanguage(w http.ResponseWriter, r *http.Request) {
	if l := i18n.Match(r.FormValue("lang")); l != "" {
		i18n.Remember(w, l, "/", web.IsHTTPS(r))
	}
	http.Redirect(w, r, web.LocalPath(r.FormValue("next")), http.StatusSeeOther)
}

// ---- invitations ----

type invitePage struct {
	Title      string
	PortalName string
	Error      string
	Usable     bool
	Username   string
	Email      string
	Base       string
	ExpiresAt  string
	MailOn     bool // a mail server is configured
	AccountOn  bool // the account pages are turned on
	Flash      string
	MailedTo   string // the address the login details are being sent to
	Path       string // the address of the page, to come back to after switching language
	expires    *time.Time
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, status int, p invitePage) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	p.PortalName = st.PortalName
	p.Base = web.BaseURL(st.PublicURL, r)
	p.MailOn = st.Mail.Configured()
	p.AccountOn = st.AccountPortal
	p.Path = r.URL.Path
	lang := h.language(r, st)
	if p.expires != nil {
		p.ExpiresAt = i18n.LongDate(lang, p.expires.Local())
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer") // keep the invite token out of referrers
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if err := h.pages[lang][name].ExecuteTemplate(w, "layout.html", p); err != nil {
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
	p := invitePage{Title: "Invitation", Usable: true, Username: r.FormValue("username"), Email: r.FormValue("email")}
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
	password := r.FormValue("password")
	u, err := h.store.RedeemInvite(r.Context(), r.PathValue("token"), r.FormValue("username"), password, r.FormValue("email"))
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
	page := invitePage{Title: "Welcome", Username: u.Username, expires: u.ExpiresAt}
	if u.Email != "" {
		st, err := h.store.Settings(r.Context())
		if err == nil && st.Mail.Configured() {
			page.MailedTo = u.Email
			// Sent in the background so a slow mail server does not hold
			// up the welcome page; failures only end up in the log. The
			// email is in the language the account was created in.
			l := mail.Login{PortalName: st.PortalName, PortalURL: web.BaseURL(st.PublicURL, r),
				Username: u.Username, Password: password, ExpiresAt: u.ExpiresAt}
			if st.AccountPortal {
				l.AccountURL = l.PortalURL + "/account"
			}
			msg := mail.LoginMessage(h.language(r, st), u.Email, l)
			go func() {
				if err := mail.Send(context.Background(), st.Mail, msg); err != nil {
					h.log.Warn("emailing login details", "username", u.Username, "err", err)
				}
			}()
		}
	}
	h.render(w, r, "welcome", http.StatusOK, page)
}
