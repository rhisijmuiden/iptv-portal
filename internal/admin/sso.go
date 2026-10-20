package admin

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/entra"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// stateCookie ties a Microsoft sign-in to the browser that started it.
const stateCookie = "iptv_sso_state"

// loginData tells the login page which sign-in options to show.
type loginData struct {
	SSO bool
}

func (h *Handler) loginPage(r *http.Request, msg string) page {
	p := page{Title: "Log in", Error: msg}
	if st, err := h.store.Settings(r.Context()); err == nil {
		p.PortalName = st.PortalName
		p.Data = loginData{SSO: st.SSO.Enabled}
	}
	return p
}

// ssoRedirectURL is the redirect URI registered in Entra: the configured
// admin panel URL, or else the address the browser used for this request.
func (h *Handler) ssoRedirectURL(c entra.Config, r *http.Request) string {
	if c.AdminURL != "" {
		return c.AdminURL + entra.CallbackPath
	}
	return web.BaseURL("", r) + h.base + entra.CallbackPath
}

func (h *Handler) ssoStart(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !st.SSO.Enabled {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	authURL, state, err := h.entra.Begin(r.Context(), st.SSO, h.ssoRedirectURL(st.SSO, r))
	if err != nil {
		h.log.Warn("starting Microsoft sign-in", "err", err)
		w.WriteHeader(http.StatusBadGateway)
		h.render(w, r, "login", h.loginPage(r, "Sign-in with Microsoft could not be started: "+err.Error()))
		return
	}
	// Lax lets the cookie come along on the top-level redirect back from
	// Microsoft.
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: h.cookiePath(),
		HttpOnly: true, Secure: web.IsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *Handler) ssoCallback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: h.cookiePath(), MaxAge: -1})
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		desc, _, _ := strings.Cut(q.Get("error_description"), "\n")
		if desc == "" {
			desc = e
		}
		h.log.Warn("Microsoft sign-in refused", "error", e, "description", desc)
		w.WriteHeader(http.StatusUnauthorized)
		h.render(w, r, "login", h.loginPage(r, "Microsoft refused the sign-in: "+strings.TrimSpace(desc)))
		return
	}
	state := q.Get("state")
	c, err := r.Cookie(stateCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		w.WriteHeader(http.StatusBadRequest)
		h.render(w, r, "login", h.loginPage(r, "The sign-in expired or was started in another browser, try again."))
		return
	}
	ip := web.ClientIP(r)
	id, err := h.entra.Finish(r.Context(), state, q.Get("code"))
	if err != nil {
		h.log.Warn("Microsoft sign-in failed", "username", id.Username, "ip", ip, "err", err)
		status := http.StatusUnauthorized
		if errors.Is(err, entra.ErrNotAllowed) {
			status = http.StatusForbidden
		}
		w.WriteHeader(status)
		h.render(w, r, "login", h.loginPage(r, err.Error()))
		return
	}
	admin, err := h.store.SSOAdmin(r.Context(), id.Username)
	if err != nil {
		h.serverError(w, err)
		return
	}
	sess, err := h.store.CreateSession(r.Context(), admin)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("admin signed in with Microsoft Entra", "username", admin.Username, "oid", id.ObjectID, "ip", ip)
	h.setSessionCookie(w, r, sess)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---- settings ----

func (h *Handler) ssoSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	c := entra.Config{
		Enabled:       r.FormValue("sso_enabled") == "on",
		TenantID:      r.FormValue("sso_tenant_id"),
		ClientID:      r.FormValue("sso_client_id"),
		ClientSecret:  r.FormValue("sso_client_secret"),
		AdminURL:      r.FormValue("sso_admin_url"),
		AllowedGroups: []string{r.FormValue("sso_allowed_groups")},
		AllowAll:      r.FormValue("sso_allow_all") == "on",
	}
	// Like the mail password, the stored secret is never sent to the
	// browser: an empty field keeps it.
	if strings.TrimSpace(c.ClientSecret) == "" {
		c.ClientSecret = st.SSO.ClientSecret
	}
	if err := h.store.SetSSO(r.Context(), c); err != nil {
		c.Validate()
		st.SSO = c
		h.render(w, r, "settings", h.settingsPage(r, sess, st, err.Error()))
		return
	}
	msg := "Microsoft sign-in settings saved."
	if !c.Enabled {
		msg = "Microsoft sign-in settings saved. Sign-in with Microsoft is off."
	}
	redirectFlash(w, r, "/settings", msg)
}
