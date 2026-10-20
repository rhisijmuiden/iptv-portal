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

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/backup"
	"github.com/rhisijmuiden/iptv-portal/internal/entra"
	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/health"
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
	activity     *activity.Tracker
	entra        *entra.Client
	backups      *backup.Service
	health       *health.Checker
	// base is the path the admin panel is served under ("" for the root).
	base string
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
	// Activity is where the portal records player requests; nil shows none.
	Activity *activity.Tracker
	// Entra runs Microsoft sign-ins; nil uses the public Entra cloud.
	Entra *entra.Client
	// Backups makes and restores backups; nil builds one for Store.
	Backups *backup.Service
	// Health checks channel streams; nil creates a checker that only runs
	// when asked.
	Health *health.Checker
	// BasePath serves the panel under this path, e.g. "/portal-admin"; it
	// must be clean (see store.CleanBasePath). Empty means the root.
	BasePath string
	Log      *slog.Logger
}

// New builds the admin panel.
func New(c Config) (*Handler, error) {
	h := &Handler{store: c.Store, listeners: c.Listeners, log: c.Log, limiter: web.NewLimiter(10, 15*time.Minute),
		guide: c.Guide, refresh: c.RefreshGuide, updater: c.Updater, restart: c.Restart, iptvorg: c.IPTVOrg,
		media: c.Media, refreshMedia: c.RefreshMedia, activity: c.Activity, entra: c.Entra, backups: c.Backups,
		health: c.Health, base: c.BasePath}
	if h.activity == nil {
		h.activity = activity.New()
	}
	if h.entra == nil {
		h.entra = entra.NewClient()
	}
	if h.health == nil {
		h.health = health.New(c.Store, c.Log)
	}
	if h.iptvorg == nil {
		h.iptvorg = iptvorg.NewClient()
	}
	if h.backups == nil && c.Store != nil {
		h.backups = backup.New(c.Store, c.Log)
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
		// base is the path the panel is served under; every link starts
		// with it.
		"base": func() string { return h.base },
		// icon draws a symbol from /static/icons.svg.
		"icon": func(name string) template.HTML {
			return template.HTML(`<svg class="icon" aria-hidden="true"><use href="` + template.HTMLEscapeString(h.base) +
				`/static/icons.svg#` + template.HTMLEscapeString(name) + `"/></svg>`)
		},
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
		"ago":  ago,
		"size": formatSize,
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
	m.HandleFunc("GET /healthz", web.Health)
	m.HandleFunc("GET /login", h.loginForm)
	m.HandleFunc("POST /login", h.login)
	m.HandleFunc("GET /login/entra", h.ssoStart)
	m.HandleFunc("GET "+entra.CallbackPath, h.ssoCallback)

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
	auth("POST /channels/{id}/check", h.channelCheck)
	auth("GET /health", h.healthPage)
	auth("POST /health", h.healthSave)
	auth("POST /health/run", h.healthRun)
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
	auth("POST /users/{id}/release", h.userRelease)
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
	auth("POST /info/channel", h.infoChannel)
	auth("POST /info/install", h.infoInstall)
	auth("GET /settings", h.settingsForm)
	auth("POST /settings", h.settingsSave)
	auth("POST /settings/mail", h.mailSave)
	auth("POST /settings/mail/test", h.mailTest)
	auth("POST /settings/sso", h.ssoSave)
	auth("POST /users/{id}/send-login", h.userSendLogin)
	auth("POST /invites/{id}/send", h.inviteSend)
	h.backupRoutes(auth)

	api := func(pattern string, fn http.HandlerFunc) {
		m.Handle(pattern, h.requireToken(fn))
	}
	api("GET /api/v1/channels", h.apiChannelList)
	api("POST /api/v1/channels", h.apiChannelCreate)
	api("PUT /api/v1/channels/{id}", h.apiChannelUpdate)
	api("DELETE /api/v1/channels/{id}", h.apiChannelDelete)
	h.mux = m
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if h.base == "" {
		h.mux.ServeHTTP(w, r)
		return
	}
	// Under a base path the reverse proxy forwards the full path, so the
	// prefix is stripped here and added again to every redirect.
	switch {
	case r.URL.Path == "/healthz":
		web.Health(w, r)
	case r.URL.Path == h.base:
		target := h.base + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	case strings.HasPrefix(r.URL.Path, h.base+"/"):
		http.StripPrefix(h.base, h.mux).ServeHTTP(&baseWriter{ResponseWriter: w, base: h.base}, r)
	default:
		http.NotFound(w, r)
	}
}

// baseWriter adds the base path to local redirects.
type baseWriter struct {
	http.ResponseWriter
	base string
}

func (w *baseWriter) WriteHeader(code int) {
	if loc := w.Header().Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		w.Header().Set("Location", w.base+loc)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *baseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *baseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// cookiePath is the path the session cookie is valid for.
func (h *Handler) cookiePath() string {
	if h.base == "" {
		return "/"
	}
	return h.base
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
	if p.Error == "" {
		p.Error = r.URL.Query().Get("err")
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
	redirectMessage(w, r, path, "ok", msg)
}

// redirectError shows msg as an error on the page at path.
func redirectError(w http.ResponseWriter, r *http.Request, path, msg string) {
	redirectMessage(w, r, path, "err", msg)
}

// redirectMessage adds msg to path as the query parameter key, replacing
// any earlier message so they do not pile up.
func redirectMessage(w http.ResponseWriter, r *http.Request, path, key, msg string) {
	u, err := url.Parse(path)
	if err != nil {
		u = &url.URL{Path: path}
	}
	q := u.Query()
	q.Del("ok")
	q.Del("err")
	q.Set(key, msg)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

// ---- login ----

func (h *Handler) loginForm(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "login", h.loginPage(r, ""))
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	ip := web.ClientIP(r)
	if !h.limiter.Allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		h.render(w, r, "login", h.loginPage(r, "Too many failed attempts, try again in 15 minutes."))
		return
	}
	admin, err := h.store.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if errors.Is(err, store.ErrInvalidLogin) {
		h.limiter.Fail(ip)
		h.log.Warn("failed login", "username", r.FormValue("username"), "ip", ip)
		w.WriteHeader(http.StatusUnauthorized)
		h.render(w, r, "login", h.loginPage(r, err.Error()))
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
	h.setSessionCookie(w, r, sess)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, sess store.Session) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sess.Token, Path: h.cookiePath(),
		HttpOnly: true, Secure: web.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(store.SessionTTL.Seconds()),
	})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.store.DeleteSession(r.Context(), sess.Token)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: h.cookiePath(), MaxAge: -1})
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
	Activity   activity.Snapshot
	Window     int // minutes a request counts as online
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
		Listeners: h.listeners.Status(), PortalURL: PortalURL(st, r),
		Activity: h.activity.Snapshot(), Window: int(activity.OnlineWindow / time.Minute)}
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

// ago describes how long before now t was, e.g. "3 min ago".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + " min ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + " h ago"
	}
	return t.Local().Format("2006-01-02 15:04")
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
	h.render(w, r, "settings", h.settingsPage(r, sess, st, ""))
}

// settingsData is what the settings page shows next to the settings.
type settingsData struct {
	store.Settings
	// SSORedirectURL is the redirect URI to register in Entra.
	SSORedirectURL string
}

func (h *Handler) settingsPage(r *http.Request, sess store.Session, st store.Settings, msg string) page {
	return page{Title: "Settings", Nav: "settings", Session: &sess, Error: msg,
		Data: settingsData{Settings: st, SSORedirectURL: h.ssoRedirectURL(st.SSO, r)}}
}

func (h *Handler) settingsSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	old, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	st := old
	st.PortalName = r.FormValue("portal_name")
	st.PublicURL = r.FormValue("public_url")
	st.AdminPort, _ = strconv.Atoi(r.FormValue("admin_port"))
	st.PortalPort, _ = strconv.Atoi(r.FormValue("portal_port"))
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
		h.render(w, r, "settings", h.settingsPage(r, sess, st, err.Error()))
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
		newAdmin = "http://" + host + ":" + strconv.Itoa(st.AdminPort) + h.base + "/"
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
