package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/rhisijmuiden/iptv-portal/internal/prefs"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// The admin side of the users' favourites and parental controls: the
// admin sees them on the user's page, can change the locks without the
// PIN, and removes the PIN when a user forgot it.

func (h *Handler) prefsRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.Session))) {
	auth("POST /users/{id}/parental", h.userParentalSave)
	auth("POST /users/{id}/parental/pin/delete", h.userParentalPINDelete)
	auth("POST /users/{id}/parental/lock", h.userParentalLock)
}

// prefsUser returns the user of the path, or answers 404.
func (h *Handler) prefsUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return store.User{}, false
	}
	u, err := h.store.User(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return u, false
	}
	if err != nil {
		h.serverError(w, err)
		return u, false
	}
	return u, true
}

func userParental(u store.User) string { return "/users/" + strconv.FormatInt(u.ID, 10) + "#parental" }

func (h *Handler) userParentalSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	u, ok := h.prefsUser(w, r)
	if !ok {
		return
	}
	if err := prefs.SaveForm(r.Context(), h.store, h.media, u, r); err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("admin changed parental controls", "admin", sess.Admin.Username, "username", u.Username)
	redirectFlash(w, r, userParental(u), "Parental controls saved.")
}

func (h *Handler) userParentalPINDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	u, ok := h.prefsUser(w, r)
	if !ok {
		return
	}
	if err := h.store.RemoveParentalPIN(r.Context(), u.ID); err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("admin removed parental control PIN", "admin", sess.Admin.Username, "username", u.Username)
	redirectFlash(w, r, userParental(u), "The PIN has been removed. The user can set a new one on their account page.")
}

func (h *Handler) userParentalLock(w http.ResponseWriter, r *http.Request, sess store.Session) {
	u, ok := h.prefsUser(w, r)
	if !ok {
		return
	}
	if err := h.store.SetUnlockedUntil(r.Context(), u.ID, nil); err != nil {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, userParental(u), "Locked again.")
}
