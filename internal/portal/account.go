package portal

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/stats"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// The account pages let users look after their own account: see until
// when it is valid and what it includes, find the details for their
// player, see what they watched, change their email address and password,
// and reset a forgotten password by email. They log in with the same
// username and password as their player.

const accountCookie = "iptv_account"

// accountPage is the data every account template receives.
type accountPage struct {
	Title      string
	Nav        string
	PortalName string
	Base       string // the portal address entered in the player
	MailOn     bool
	Session    *store.UserSession
	Flash      string
	Error      string
	Path       string
	Data       any
}

func (h *Handler) accountRoutes() {
	h.mux.HandleFunc("GET /account/login", h.accountOn(h.accountLoginForm))
	h.mux.HandleFunc("POST /account/login", h.accountOn(h.accountLogin))
	h.mux.HandleFunc("GET /account/forgot", h.accountOn(h.forgotForm))
	h.mux.HandleFunc("POST /account/forgot", h.accountOn(h.forgotSend))
	h.mux.HandleFunc("GET /account/reset/{token}", h.accountOn(h.resetForm))
	h.mux.HandleFunc("POST /account/reset/{token}", h.accountOn(h.resetSave))

	auth := func(pattern string, fn func(http.ResponseWriter, *http.Request, store.UserSession)) {
		h.mux.HandleFunc(pattern, h.accountOn(h.requireAccount(fn)))
	}
	auth("GET /account", h.accountOverview)
	auth("GET /account/{$}", h.accountOverview)
	auth("GET /account/setup", h.accountSetup)
	auth("GET /account/history", h.accountHistory)
	auth("GET /account/settings", h.accountSettings)
	auth("POST /account/email", h.accountEmail)
	auth("POST /account/password", h.accountPassword)
	auth("POST /account/logout", h.accountLogout)
}

// accountOn answers 404 while the admin has the account pages turned off.
func (h *Handler) accountOn(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, err := h.store.Settings(r.Context())
		if err != nil {
			h.serverError(w, err)
			return
		}
		if !st.AccountPortal {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer") // keep reset tokens out of referrers
		fn(w, r)
	}
}

// requireAccount checks the account cookie and, for POST requests, the
// CSRF token.
func (h *Handler) requireAccount(fn func(http.ResponseWriter, *http.Request, store.UserSession)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(accountCookie)
		if err != nil {
			http.Redirect(w, r, "/account/login", http.StatusSeeOther)
			return
		}
		sess, err := h.store.UserSession(r.Context(), c.Value)
		if errors.Is(err, store.ErrNotFound) {
			http.Redirect(w, r, "/account/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(sess.CSRF)) != 1 {
				http.Error(w, "session expired, reload the page", http.StatusForbidden)
				return
			}
		}
		fn(w, r, sess)
	}
}

func (h *Handler) renderAccount(w http.ResponseWriter, r *http.Request, name string, status int, p accountPage) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	lang := h.language(r, st)
	p.PortalName = st.PortalName
	p.Base = web.BaseURL(st.PublicURL, r)
	p.MailOn = st.Mail.Configured()
	if p.Flash == "" {
		p.Flash = r.URL.Query().Get("ok")
	}
	p.Flash = i18n.T(lang, p.Flash)
	p.Error = i18n.T(lang, web.Sentence(p.Error))
	p.Path = r.URL.Path
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.pages[lang][name].ExecuteTemplate(w, "layout.html", p); err != nil {
		h.log.Error("rendering template", "page", name, "err", err)
	}
}

// redirectFlash goes to path and shows msg there.
func redirectFlash(w http.ResponseWriter, r *http.Request, path, msg string) {
	http.Redirect(w, r, path+"?"+url.Values{"ok": {msg}}.Encode(), http.StatusSeeOther)
}

func (h *Handler) setAccountCookie(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	http.SetCookie(w, &http.Cookie{
		Name: accountCookie, Value: sess.Token, Path: "/account",
		HttpOnly: true, Secure: web.IsHTTPS(r), SameSite: http.SameSiteLaxMode,
		MaxAge: int(store.UserSessionTTL.Seconds()),
	})
}

// ---- login ----

func (h *Handler) accountLoginForm(w http.ResponseWriter, r *http.Request) {
	h.renderAccount(w, r, "login", http.StatusOK, accountPage{Title: "Log in"})
}

func (h *Handler) accountLogin(w http.ResponseWriter, r *http.Request) {
	ip := web.ClientIP(r)
	p := accountPage{Title: "Log in", Data: r.FormValue("username")}
	if !h.limiter.Allow(ip) {
		p.Error = "Too many attempts, try again in 15 minutes."
		h.renderAccount(w, r, "login", http.StatusTooManyRequests, p)
		return
	}
	u, err := h.store.LoginUser(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if errors.Is(err, store.ErrInvalidLogin) {
		h.limiter.Fail(ip)
		h.log.Warn("failed account login", "username", r.FormValue("username"), "ip", ip)
		p.Error = err.Error()
		h.renderAccount(w, r, "login", http.StatusUnauthorized, p)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.limiter.Reset(ip)
	sess, err := h.store.CreateUserSession(r.Context(), u)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.setAccountCookie(w, r, sess)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

func (h *Handler) accountLogout(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	h.store.DeleteUserSession(r.Context(), sess.Token)
	http.SetCookie(w, &http.Cookie{Name: accountCookie, Path: "/account", MaxAge: -1})
	redirectFlash(w, r, "/account/login", "You have been logged out.")
}

// ---- overview ----

type overviewData struct {
	User     store.User
	TV       int // TV channels in the user's packages
	Radio    int
	OnDemand bool
	DaysLeft int // days until the account expires, rounded up; only with an expiry date
	Watched  store.ViewSummary
	Now      time.Time
}

func (h *Handler) accountOverview(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	d := overviewData{User: sess.User, Now: time.Now()}
	chs, err := h.store.UserChannels(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	for _, c := range chs {
		if c.IsRadio() {
			d.Radio++
		} else {
			d.TV++
		}
	}
	if d.OnDemand, err = h.store.UserOnDemand(ctx, sess.User.ID); err != nil {
		h.serverError(w, err)
		return
	}
	if exp := sess.User.ExpiresAt; exp != nil && exp.After(d.Now) {
		d.DaysLeft = int(math.Ceil(exp.Sub(d.Now).Hours() / 24))
	}
	if d.Watched, err = h.store.ViewSummary(ctx, store.ViewFilter{UserID: sess.User.ID, Since: d.Now.AddDate(0, 0, -30)}); err != nil {
		h.serverError(w, err)
		return
	}
	h.renderAccount(w, r, "account", http.StatusOK, accountPage{Title: "My account", Nav: "account", Session: &sess, Data: d})
}

func (h *Handler) accountSetup(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	h.renderAccount(w, r, "setup", http.StatusOK, accountPage{Title: "Set up your TV", Nav: "setup", Session: &sess})
}

// ---- viewing history ----

// historyPeriods are the periods the history page offers, in days.
var historyPeriods = []int{7, 30, 90}

type historyData struct {
	Days     int
	Periods  []int
	Summary  store.ViewSummary
	Chart    []historyBar
	Channels []store.ChannelViews
	Recent   []store.View
	Assume   int // minutes a view is assumed to last
	Now      time.Time
}

type historyBar struct {
	store.DayViews
	Height int // percent of the highest bar
}

func (h *Handler) accountHistory(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	d := historyData{Days: 30, Periods: historyPeriods, Assume: int(stats.Assume / time.Minute), Now: time.Now()}
	if n, err := strconv.Atoi(r.FormValue("days")); err == nil {
		for _, p := range historyPeriods {
			if n == p {
				d.Days = n
			}
		}
	}
	today := time.Date(d.Now.Year(), d.Now.Month(), d.Now.Day(), 0, 0, 0, 0, time.Local)
	f := store.ViewFilter{UserID: sess.User.ID, Since: today.AddDate(0, 0, 1-d.Days)}
	var err error
	if d.Summary, err = h.store.ViewSummary(ctx, f); err != nil {
		h.serverError(w, err)
		return
	}
	days, err := h.store.ViewsByDay(ctx, f, d.Now)
	if err != nil {
		h.serverError(w, err)
		return
	}
	highest := 0
	for _, day := range days {
		highest = max(highest, day.Views)
	}
	for _, day := range days {
		b := historyBar{DayViews: day}
		if highest > 0 {
			b.Height = day.Views * 100 / highest
		}
		d.Chart = append(d.Chart, b)
	}
	if d.Channels, err = h.store.ChannelViews(ctx, f); err != nil {
		h.serverError(w, err)
		return
	}
	if d.Recent, err = h.store.RecentViews(ctx, f, 25); err != nil {
		h.serverError(w, err)
		return
	}
	h.renderAccount(w, r, "history", http.StatusOK, accountPage{Title: "Viewing history", Nav: "history", Session: &sess, Data: d})
}

// ---- settings ----

func (h *Handler) accountSettings(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	h.renderAccount(w, r, "profile", http.StatusOK, accountPage{Title: "Account settings", Nav: "settings", Session: &sess,
		Data: sess.User.Email})
}

func (h *Handler) accountEmail(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	email := strings.TrimSpace(r.FormValue("email"))
	if err := h.store.SetUserEmail(r.Context(), sess.User.ID, email); err != nil {
		h.renderAccount(w, r, "profile", http.StatusBadRequest, accountPage{Title: "Account settings", Nav: "settings",
			Session: &sess, Error: err.Error(), Data: email})
		return
	}
	h.log.Info("user changed email address", "username", sess.User.Username)
	if email == "" {
		redirectFlash(w, r, "/account/settings", "Your email address has been removed.")
		return
	}
	redirectFlash(w, r, "/account/settings", "Your email address has been saved.")
}

func (h *Handler) accountPassword(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	p := accountPage{Title: "Account settings", Nav: "settings", Session: &sess, Data: sess.User.Email}
	ip := web.ClientIP(r)
	if !h.limiter.Allow(ip) {
		p.Error = "Too many attempts, try again in 15 minutes."
		h.renderAccount(w, r, "profile", http.StatusTooManyRequests, p)
		return
	}
	if r.FormValue("new") != r.FormValue("confirm") {
		p.Error = "The new passwords do not match."
		h.renderAccount(w, r, "profile", http.StatusBadRequest, p)
		return
	}
	if err := h.store.ChangeUserPassword(r.Context(), sess, r.FormValue("current"), r.FormValue("new")); err != nil {
		h.limiter.Fail(ip) // slows down guessing of the current password
		p.Error = err.Error()
		h.renderAccount(w, r, "profile", http.StatusBadRequest, p)
		return
	}
	h.log.Info("user changed password", "username", sess.User.Username, "ip", ip)
	h.notifyPasswordChanged(r, sess.User)
	redirectFlash(w, r, "/account/settings", "Your password has been changed. Enter the new password in your TV box or IPTV app as well.")
}

// notifyPasswordChanged emails the user that their password changed, so
// they notice when someone else did it.
func (h *Handler) notifyPasswordChanged(r *http.Request, u store.User) {
	st, err := h.store.Settings(r.Context())
	if err != nil || u.Email == "" || !st.Mail.Configured() {
		return
	}
	base := web.BaseURL(st.PublicURL, r)
	msg := mail.PasswordChangedMessage(h.language(r, st), u.Email, st.PortalName, u.Username, base+"/account/forgot")
	go func() {
		if err := mail.Send(context.Background(), st.Mail, msg); err != nil {
			h.log.Warn("emailing password change notice", "username", u.Username, "err", err)
		}
	}()
}

// ---- forgotten password ----

func (h *Handler) forgotForm(w http.ResponseWriter, r *http.Request) {
	h.renderAccount(w, r, "forgot", http.StatusOK, accountPage{Title: "Forgot your password?"})
}

func (h *Handler) forgotSend(w http.ResponseWriter, r *http.Request) {
	ip := web.ClientIP(r)
	login := strings.TrimSpace(r.FormValue("login"))
	p := accountPage{Title: "Forgot your password?", Data: login}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !st.Mail.Configured() {
		h.renderAccount(w, r, "forgot", http.StatusOK, p)
		return
	}
	// Every request counts, so the form cannot be used to flood
	// someone's mailbox.
	if !h.limiter.Allow(ip) {
		p.Error = "Too many attempts, try again in 15 minutes."
		h.renderAccount(w, r, "forgot", http.StatusTooManyRequests, p)
		return
	}
	h.limiter.Fail(ip)
	if login == "" {
		p.Error = "Enter your username or email address."
		h.renderAccount(w, r, "forgot", http.StatusBadRequest, p)
		return
	}
	resets, err := h.store.CreatePasswordResets(r.Context(), login)
	if err != nil {
		h.serverError(w, err)
		return
	}
	lang := h.language(r, st)
	base := web.BaseURL(st.PublicURL, r)
	for _, rs := range resets {
		msg := mail.PasswordResetMessage(lang, rs.User.Email, st.PortalName, rs.User.Username,
			base+"/account/reset/"+rs.Token, store.PasswordResetTTL)
		go func() {
			if err := mail.Send(context.Background(), st.Mail, msg); err != nil {
				h.log.Warn("emailing password reset link", "username", rs.User.Username, "err", err)
			}
		}()
		h.log.Info("password reset requested", "username", rs.User.Username, "ip", ip)
	}
	// The same answer whether or not an account was found, so the form
	// does not tell which usernames and addresses exist.
	p.Flash = "If an account with this username or email address exists and has an email address, we have sent it a link to choose a new password. Check your inbox and spam folder."
	h.renderAccount(w, r, "forgot", http.StatusOK, accountPage{Title: p.Title, Flash: p.Flash, Data: "sent"})
}

func (h *Handler) resetForm(w http.ResponseWriter, r *http.Request) {
	u, err := h.store.PasswordResetUser(r.Context(), r.PathValue("token"))
	if err != nil && !errors.Is(err, store.ErrResetInvalid) {
		h.serverError(w, err)
		return
	}
	if err != nil {
		h.renderAccount(w, r, "reset", http.StatusNotFound, accountPage{Title: "Choose a new password"})
		return
	}
	h.renderAccount(w, r, "reset", http.StatusOK, accountPage{Title: "Choose a new password", Data: u.Username})
}

func (h *Handler) resetSave(w http.ResponseWriter, r *http.Request) {
	ip := web.ClientIP(r)
	token := r.PathValue("token")
	p := accountPage{Title: "Choose a new password"}
	if !h.limiter.Allow(ip) {
		p.Error = "Too many attempts, try again in 15 minutes."
		h.renderAccount(w, r, "reset", http.StatusTooManyRequests, p)
		return
	}
	u, err := h.store.PasswordResetUser(r.Context(), token)
	if errors.Is(err, store.ErrResetInvalid) {
		h.limiter.Fail(ip) // slows down guessing of tokens
		h.renderAccount(w, r, "reset", http.StatusNotFound, p)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	p.Data = u.Username
	if r.FormValue("password") != r.FormValue("confirm") {
		p.Error = "The passwords do not match."
		h.renderAccount(w, r, "reset", http.StatusBadRequest, p)
		return
	}
	if u, err = h.store.ResetUserPasswordWithToken(r.Context(), token, r.FormValue("password")); err != nil {
		if errors.Is(err, store.ErrResetInvalid) { // used in the meantime
			h.renderAccount(w, r, "reset", http.StatusNotFound, accountPage{Title: p.Title})
			return
		}
		p.Error = err.Error()
		h.renderAccount(w, r, "reset", http.StatusBadRequest, p)
		return
	}
	h.log.Info("password reset with emailed link", "username", u.Username, "ip", ip)
	sess, err := h.store.CreateUserSession(r.Context(), u)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.setAccountCookie(w, r, sess)
	redirectFlash(w, r, "/account", "Your password has been changed. Enter the new password in your TV box or IPTV app as well.")
}
