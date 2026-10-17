// Package admin serves the admin panel: login, channel and category
// management, playlist import, media servers and settings.
package admin

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/iptvorg"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/server"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/update"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

//go:embed templates/*.html
var templateFS embed.FS

const sessionCookie = "iptv_admin"

// Listeners is the part of server.Manager the admin panel uses.
type Listeners interface {
	Start(adminPort, portalPort int)
	Status() []server.Status
	PortAvailable(port int) error
}

// Handler is the admin panel HTTP handler.
type Handler struct {
	store        *store.Store
	listeners    Listeners
	log          *slog.Logger
	pages        map[string]*template.Template
	mux          *http.ServeMux
	limiter      *web.Limiter
	guide        *epg.Guide
	refresh      chan<- struct{}
	updater      *update.Updater
	restart      func()
	iptvorg      *iptvorg.Client
	media        *media.Catalog
	refreshMedia chan<- struct{}
}

// Config holds what the admin panel needs from the rest of the program.
type Config struct {
	Store     *store.Store
	Listeners Listeners
	Guide     *epg.Guide
	// RefreshGuide receives a value when the guide should reload.
	RefreshGuide chan<- struct{}
	Updater      *update.Updater
	// Restart stops the service and starts the (updated) binary again.
	Restart func()
	// IPTVOrg reads the iptv-org channel list; nil uses the public API.
	IPTVOrg *iptvorg.Client
	// Media is the On Demand catalog; RefreshMedia receives a value when
	// it should reload.
	Media        *media.Catalog
	RefreshMedia chan<- struct{}
	Log          *slog.Logger
}

// New builds the admin panel.
func New(c Config) (*Handler, error) {
	h := &Handler{store: c.Store, listeners: c.Listeners, log: c.Log, limiter: web.NewLimiter(10, 15*time.Minute),
		guide: c.Guide, refresh: c.RefreshGuide, updater: c.Updater, restart: c.Restart, iptvorg: c.IPTVOrg,
		media: c.Media, refreshMedia: c.RefreshMedia}
	if h.iptvorg == nil {
		h.iptvorg = iptvorg.NewClient()
	}
	if h.media == nil {
		h.media = media.NewCatalog(c.Store, c.Log)
	}
	if err := h.parseTemplates(); err != nil {
		return nil, err
	}
	h.routes()
	return h, nil
}

func (h *Handler) parseTemplates() error {
	funcs := template.FuncMap{
		"version": func() string { return version.Version },
		"has": func(ids []int64, id int64) bool {
			for _, v := range ids {
				if v == id {
					return true
				}
			}
			return false
		},
		// date formats an optional time for display; "" for nil.
		"date": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("2006-01-02")
		},
		"ptr":   func(t time.Time) *time.Time { return &t },
		"notes": update.RenderNotes,
		"datetime": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.Local().Format("2006-01-02 15:04")
		},
	}
	layout, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html")
	if err != nil {
		return err
	}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	h.pages = map[string]*template.Template{}
	for _, n := range names {
		name := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		t, err := template.Must(layout.Clone()).ParseFS(templateFS, n)
		if err != nil {
			return err
		}
		h.pages[name] = t
	}
	return nil
}

func (h *Handler) routes() {
	m := http.NewServeMux()
	m.Handle("GET /static/", web.Static())
	m.HandleFunc("GET /login", h.loginForm)
	m.HandleFunc("POST /login", h.login)

	auth := func(pattern string, fn func(http.ResponseWriter, *http.Request, store.Session)) {
		m.Handle(pattern, h.requireSession(fn))
	}
	auth("POST /logout", h.logout)
	auth("GET /password", h.passwordForm)
	auth("POST /password", h.changePassword)
	auth("GET /{$}", h.dashboard)
	auth("GET /channels", h.channelList)
	auth("GET /channels/new", h.channelForm)
	auth("POST /channels/new", h.channelSave)
	auth("GET /channels/{id}", h.channelForm)
	auth("POST /channels/{id}", h.channelSave)
	auth("POST /channels/{id}/toggle", h.channelToggle)
	auth("POST /channels/{id}/delete", h.channelDelete)
	auth("GET /categories", h.categoryList)
	auth("POST /categories", h.categorySave)
	auth("POST /categories/{id}/delete", h.categoryDelete)
	auth("GET /import", h.importForm)
	auth("POST /import", h.importRun)
	auth("GET /iptv-org", h.iptvorgPage)
	auth("POST /iptv-org/add", h.iptvorgAdd)
	auth("POST /iptv-org/reload", h.iptvorgReload)
	auth("GET /users", h.userList)
	auth("GET /users/new", h.userForm)
	auth("POST /users/new", h.userSave)
	auth("GET /users/{id}", h.userForm)
	auth("POST /users/{id}", h.userSave)
	auth("POST /users/{id}/toggle", h.userToggle)
	auth("POST /users/{id}/delete", h.userDelete)
	auth("GET /packages", h.packageList)
	auth("GET /packages/new", h.packageForm)
	auth("POST /packages/new", h.packageSave)
	auth("GET /packages/{id}", h.packageForm)
	auth("POST /packages/{id}", h.packageSave)
	auth("POST /packages/{id}/delete", h.packageDelete)
	auth("GET /invites", h.inviteList)
	auth("POST /invites", h.inviteCreate)
	auth("POST /invites/{id}/revoke", h.inviteRevoke)
	auth("POST /invites/{id}/delete", h.inviteDelete)
	auth("GET /media", h.mediaList)
	auth("GET /media/new", h.mediaForm)
	auth("POST /media/new", h.mediaSave)
	auth("GET /media/{id}", h.mediaForm)
	auth("POST /media/{id}", h.mediaSave)
	auth("POST /media/{id}/delete", h.mediaDelete)
	auth("POST /media/refresh", h.mediaRefresh)
	auth("GET /epg", h.epgPage)
	auth("POST /epg", h.epgSave)
	auth("POST /epg/refresh", h.epgRefresh)
	auth("GET /info", h.infoPage)
	auth("POST /info/check", h.infoCheck)
	auth("POST /info/auto", h.infoAuto)
	auth("POST /info/install", h.infoInstall)
	auth("GET /settings", h.settingsForm)
	auth("POST /settings", h.settingsSave)
	h.mux = m
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	h.mux.ServeHTTP(w, r)
}

// requireSession checks the login cookie and, for POST requests, the CSRF
// token. Admins with a one-time password can only reach the password page.
func (h *Handler) requireSession(fn func(http.ResponseWriter, *http.Request, store.Session)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			h.redirectLogin(w, r)
			return
		}
		sess, err := h.store.Session(r.Context(), c.Value)
		if errors.Is(err, store.ErrNotFound) {
			h.redirectLogin(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
			if err := r.ParseMultipartForm(20 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(sess.CSRF)) != 1 {
				http.Error(w, "session expired, reload the page", http.StatusForbidden)
				return
			}
		}
		if sess.Admin.MustChangePassword && r.URL.Path != "/password" && r.URL.Path != "/logout" {
			http.Redirect(w, r, "/password", http.StatusSeeOther)
			return
		}
		fn(w, r, sess)
	})
}

func (h *Handler) redirectLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// page is the data every template receives.
type page struct {
	Title      string
	Nav        string
	PortalName string
	Session    *store.Session
	Flash      string
	Error      string
	Data       any
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, p page) {
	t, ok := h.pages[name]
	if !ok {
		h.serverError(w, errors.New("unknown template "+name))
		return
	}
	if p.PortalName == "" {
		if st, err := h.store.Settings(r.Context()); err == nil {
			p.PortalName = st.PortalName
		}
	}
	if p.Flash == "" {
		p.Flash = r.URL.Query().Get("ok")
	}
	p.Error = web.Sentence(p.Error)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := t.ExecuteTemplate(w, "layout.html", p); err != nil {
		h.log.Error("rendering template", "page", name, "err", err)
	}
}

func (h *Handler) serverError(w http.ResponseWriter, err error) {
	h.log.Error("internal error", "err", err)
	http.Error(w, "Something went wrong, see the server log.", http.StatusInternalServerError)
}

func redirectFlash(w http.ResponseWriter, r *http.Request, path, msg string) {
	http.Redirect(w, r, path+"?ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

// ---- login ----

func (h *Handler) loginForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "login", page{Title: "Log in"})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	ip := web.ClientIP(r)
	if !h.limiter.Allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		h.render(w, r, "login", page{Title: "Log in", Error: "Too many failed attempts, try again in 15 minutes."})
		return
	}
	admin, err := h.store.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if errors.Is(err, store.ErrInvalidLogin) {
		h.limiter.Fail(ip)
		h.log.Warn("failed login", "username", r.FormValue("username"), "ip", ip)
		w.WriteHeader(http.StatusUnauthorized)
		h.render(w, r, "login", page{Title: "Log in", Error: err.Error()})
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.limiter.Reset(ip)
	sess, err := h.store.CreateSession(r.Context(), admin)
	if err != nil {
		h.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sess.Token, Path: "/",
		HttpOnly: true, Secure: web.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(store.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.store.DeleteSession(r.Context(), sess.Token)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handler) passwordForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.render(w, r, "password", page{Title: "Change password", Session: &sess})
}

func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, sess store.Session) {
	p := page{Title: "Change password", Session: &sess}
	if r.FormValue("new") != r.FormValue("confirm") {
		p.Error = "The new passwords do not match."
		h.render(w, r, "password", p)
		return
	}
	if err := h.store.ChangeOwnPassword(r.Context(), sess, r.FormValue("current"), r.FormValue("new")); err != nil {
		p.Error = err.Error()
		h.render(w, r, "password", p)
		return
	}
	redirectFlash(w, r, "/", "Your password has been changed.")
}

// ---- dashboard ----

type dashboardData struct {
	Channels   int
	Enabled    int
	Categories int
	Users      int
	Active     int
	Listeners  []server.Status
	PortalURL  string
	Update     *update.Release // newer release, if one was found
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request, sess store.Session) {
	chs, err := h.store.Channels(r.Context(), store.ChannelFilter{})
	if err != nil {
		h.serverError(w, err)
		return
	}
	cats, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	active, total, err := h.store.CountActiveUsers(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	d := dashboardData{Channels: len(chs), Categories: len(cats), Users: total, Active: active,
		Listeners: h.listeners.Status(), PortalURL: PortalURL(st, r)}
	if us := h.updater.Status(); us.Available() {
		d.Update = us.Latest()
	}
	for _, c := range chs {
		if c.Enabled {
			d.Enabled++
		}
	}
	h.render(w, r, "dashboard", page{Title: "Dashboard", Nav: "dashboard", Session: &sess, Data: d})
}

// PortalURL is the address clients enter in their player. It is the public
// URL when set, otherwise the host of the current request on the portal
// port.
func PortalURL(st store.Settings, r *http.Request) string {
	if st.PublicURL != "" {
		return st.PublicURL
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if strings.Contains(host, ":") { // bare IPv6
		host = "[" + host + "]"
	}
	if st.PortalPort == 80 {
		return "http://" + host
	}
	return "http://" + host + ":" + strconv.Itoa(st.PortalPort)
}

// ---- settings ----

func (h *Handler) settingsForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "settings", page{Title: "Settings", Nav: "settings", Session: &sess, Data: st})
}

func (h *Handler) settingsSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	old, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	st := store.Settings{
		PortalName: r.FormValue("portal_name"),
		PublicURL:  r.FormValue("public_url"),
		EPGURLs:    old.EPGURLs,

		AutoUpdateCheck: old.AutoUpdateCheck,
	}
	st.AdminPort, _ = strconv.Atoi(r.FormValue("admin_port"))
	st.PortalPort, _ = strconv.Atoi(r.FormValue("portal_port"))
	p := page{Title: "Settings", Nav: "settings", Session: &sess, Data: st}
	err = st.Validate()
	if err == nil && st.AdminPort != old.AdminPort {
		err = h.listeners.PortAvailable(st.AdminPort)
	}
	if err == nil && st.PortalPort != old.PortalPort {
		err = h.listeners.PortAvailable(st.PortalPort)
	}
	if err == nil {
		err = h.store.SaveSettings(r.Context(), st)
	}
	if err != nil {
		p.Error = err.Error()
		h.render(w, r, "settings", p)
		return
	}
	if st.AdminPort == old.AdminPort && st.PortalPort == old.PortalPort {
		redirectFlash(w, r, "/settings", "Settings saved.")
		return
	}
	// The listeners are restarted after this response has been sent, because
	// shutting down the admin listener waits for this very request.
	go func() {
		time.Sleep(500 * time.Millisecond)
		h.listeners.Start(st.AdminPort, st.PortalPort)
	}()
	newAdmin := ""
	if st.AdminPort != old.AdminPort {
		host := r.Host
		if hst, _, err := net.SplitHostPort(host); err == nil {
			host = hst
		}
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		newAdmin = "http://" + host + ":" + strconv.Itoa(st.AdminPort) + "/"
	}
	h.render(w, r, "restarted", page{Title: "Settings saved", Nav: "settings", Session: &sess,
		Data: map[string]any{"Settings": st, "NewAdminURL": newAdmin}})
}

// PurgeLoop removes expired sessions periodically until ctx is done.
func (h *Handler) PurgeLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := h.store.PurgeExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			h.log.Warn("purging sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
