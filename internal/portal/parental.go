package portal

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/prefs"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// Parental controls: with a PIN, users lock channel groups and On Demand
// categories, and set an age limit for On Demand. Players do not get
// what is locked until someone unlocks it for a while with the PIN on
// this page. The settings themselves also need the PIN, because the
// account login is the same as the player's.

// unlockHours are the unlock periods offered.
var unlockHours = []int{1, 3, 12}

func (h *Handler) parentalRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.UserSession))) {
	auth("GET /account/parental", h.accountParental)
	auth("POST /account/parental/pin", h.parentalSetPIN)
	auth("POST /account/parental/open", h.parentalOpen)
	auth("POST /account/parental/close", h.parentalClose)
	auth("POST /account/parental/save", h.parentalSave)
	auth("POST /account/parental/unlock", h.parentalUnlock)
	auth("POST /account/parental/lock", h.parentalLock)
}

type parentalData struct {
	prefs.Form
	Open      bool // the PIN was entered in this session
	OpenUntil time.Time
	Hours     []int
	Now       time.Time
}

func (h *Handler) accountParental(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	h.renderParental(w, r, sess, http.StatusOK, "")
}

func (h *Handler) renderParental(w http.ResponseWriter, r *http.Request, sess store.UserSession, status int, errMsg string) {
	f, err := prefs.FormData(r.Context(), h.store, h.vod, sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	now := time.Now()
	d := parentalData{Form: f, Open: f.PINSet && sess.PINOpen(now), Hours: unlockHours, Now: now}
	if d.Open {
		d.OpenUntil = *sess.PINUntil
	}
	h.renderAccount(w, r, "parental", status, accountPage{Title: "Parental controls", Nav: "parental", Session: &sess,
		Error: errMsg, Data: d})
}

// pinKey limits PIN guesses per account, from any address.
func pinKey(u store.User) string { return "user:" + strconv.FormatInt(u.ID, 10) }

// openPIN marks the session as having entered the PIN.
func (h *Handler) openPIN(ctx context.Context, sess *store.UserSession) error {
	until := time.Now().Add(store.PINSessionTTL)
	sess.PINUntil = &until
	return h.store.SetSessionPIN(ctx, sess.Token, &until)
}

// parentalSetPIN sets the first PIN, or changes it while the settings are
// open.
func (h *Handler) parentalSetPIN(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	prefs, err := h.store.Preferences(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if prefs.Parental.PINSet && !sess.PINOpen(time.Now()) {
		h.renderParental(w, r, sess, http.StatusForbidden, "Enter your PIN first.")
		return
	}
	pin := r.FormValue("pin")
	if pin != r.FormValue("confirm") {
		h.renderParental(w, r, sess, http.StatusBadRequest, "The PINs do not match.")
		return
	}
	if err := h.store.SetParentalPIN(ctx, sess.User.ID, pin); err != nil {
		if errors.Is(err, store.ErrPINFormat) {
			h.renderParental(w, r, sess, http.StatusBadRequest, err.Error())
			return
		}
		h.serverError(w, err)
		return
	}
	if err := h.openPIN(ctx, &sess); err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("user set parental control PIN", "username", sess.User.Username, "changed", prefs.Parental.PINSet)
	msg := "Your PIN has been set. Choose below what to lock."
	if prefs.Parental.PINSet {
		msg = "Your PIN has been changed."
	}
	redirectFlash(w, r, "/account/parental", msg)
}

func (h *Handler) parentalOpen(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	key := pinKey(sess.User)
	ip := web.ClientIP(r)
	if !h.pinLimiter.Allow(key) || !h.limiter.Allow(ip) {
		h.renderParental(w, r, sess, http.StatusTooManyRequests, "Too many attempts, try again in 15 minutes.")
		return
	}
	err := h.store.CheckParentalPIN(ctx, sess.User.ID, r.FormValue("pin"))
	switch {
	case errors.Is(err, store.ErrPINWrong):
		h.pinLimiter.Fail(key)
		h.limiter.Fail(ip)
		h.log.Warn("wrong parental control PIN", "username", sess.User.Username, "ip", ip)
		h.renderParental(w, r, sess, http.StatusUnauthorized, err.Error())
		return
	case errors.Is(err, store.ErrPINNeeded):
		h.renderParental(w, r, sess, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		h.serverError(w, err)
		return
	}
	h.pinLimiter.Reset(key)
	if err := h.openPIN(ctx, &sess); err != nil {
		h.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/account/parental", http.StatusSeeOther)
}

func (h *Handler) parentalClose(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	if err := h.store.SetSessionPIN(r.Context(), sess.Token, nil); err != nil {
		h.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/account/parental", http.StatusSeeOther)
}

// requirePIN answers with the PIN form unless the PIN was entered in this
// session.
func (h *Handler) requirePIN(w http.ResponseWriter, r *http.Request, sess store.UserSession) bool {
	prefs, err := h.store.Preferences(r.Context(), sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return false
	}
	if !prefs.Parental.PINSet {
		h.renderParental(w, r, sess, http.StatusBadRequest, store.ErrPINNeeded.Error())
		return false
	}
	if !sess.PINOpen(time.Now()) {
		h.renderParental(w, r, sess, http.StatusForbidden, "Enter your PIN first.")
		return false
	}
	return true
}

func (h *Handler) parentalSave(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	if !h.requirePIN(w, r, sess) {
		return
	}
	if err := prefs.SaveForm(r.Context(), h.store, h.vod, sess.User, r); err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("user changed parental controls", "username", sess.User.Username)
	redirectFlash(w, r, "/account/parental", "Parental controls saved. Refresh the channel list in your player to see the change.")
}

func (h *Handler) parentalUnlock(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	if !h.requirePIN(w, r, sess) {
		return
	}
	hours, _ := strconv.Atoi(r.FormValue("hours"))
	if !slices.Contains(unlockHours, hours) {
		hours = unlockHours[0]
	}
	until := time.Now().Add(time.Duration(hours) * time.Hour)
	if err := h.store.SetUnlockedUntil(r.Context(), sess.User.ID, &until); err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("user unlocked parental controls", "username", sess.User.Username, "hours", hours)
	redirectFlash(w, r, "/account/parental", "Unlocked. Refresh the channel list in your player to see the locked channels.")
}

// parentalLock locks everything again; this needs no PIN.
func (h *Handler) parentalLock(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	if err := h.store.SetUnlockedUntil(r.Context(), sess.User.ID, nil); err != nil {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/account/parental", "Locked again.")
}
