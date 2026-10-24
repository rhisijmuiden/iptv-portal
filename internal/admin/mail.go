package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// ---- mail server settings ----

func (h *Handler) mailSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	m := mail.Config{
		Host:     r.FormValue("smtp_host"),
		Security: mail.Security(r.FormValue("smtp_security")),
		Username: r.FormValue("smtp_username"),
		Password: r.FormValue("smtp_password"),
		From:     r.FormValue("smtp_from"),
		FromName: r.FormValue("smtp_from_name"),
	}
	if v := strings.TrimSpace(r.FormValue("smtp_port")); v != "" {
		if m.Port, err = strconv.Atoi(v); err != nil {
			m.Port = -1 // reported by Validate
		}
	}
	// The stored password is never sent to the browser: an empty field
	// keeps it, unless the login is removed altogether.
	if m.Password == "" && strings.TrimSpace(m.Username) != "" && r.FormValue("smtp_clear_password") != "on" {
		m.Password = st.Mail.Password
	}
	if strings.TrimSpace(m.Username) == "" {
		m.Password = ""
	}
	if err := h.store.SetMail(r.Context(), m); err != nil {
		st.Mail = m
		h.render(w, r, "settings", h.settingsPage(r, sess, st, err.Error()))
		return
	}
	msg := "Mail settings saved."
	if strings.TrimSpace(m.Host) == "" {
		msg = "Mail settings saved. Sending email is off."
	}
	redirectFlash(w, r, "/settings", msg)
}

func (h *Handler) mailTest(w http.ResponseWriter, r *http.Request, sess store.Session) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	to := r.FormValue("to")
	if err := mail.Send(r.Context(), st.Mail, mail.TestMessage(i18n.FromContext(r.Context()), to, st.PortalName)); err != nil {
		h.log.Warn("sending test email", "to", to, "err", err)
		h.render(w, r, "settings", h.settingsPage(r, sess, st, tr(r, "The test email could not be sent: %s", err.Error())))
		return
	}
	redirectFlash(w, r, "/settings", tr(r, "Test email sent to %s.", strings.TrimSpace(to)))
}

// ---- sending login details and invitations ----

// sendLogin emails a user the details for their player.
func (h *Handler) sendLogin(r *http.Request, st store.Settings, u store.User, password string) error {
	if u.Email == "" {
		return errors.New("this user has no email address")
	}
	msg := mail.LoginMessage(st.Language, u.Email, mail.Login{PortalName: st.PortalName, PortalURL: PortalURL(st, r),
		Username: u.Username, Password: password, ExpiresAt: u.ExpiresAt})
	if err := mail.Send(r.Context(), st.Mail, msg); err != nil {
		h.log.Warn("emailing login details", "username", u.Username, "err", err)
		return err
	}
	h.log.Info("emailed login details", "username", u.Username, "to", u.Email)
	return nil
}

// userSendLogin gives a user a new password and emails it, because the
// current one is only stored as a hash.
func (h *Handler) userSendLogin(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, err := h.store.User(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !st.Mail.Configured() || u.Email == "" {
		h.renderUserForm(w, r, sess, u, "Set up a mail server and an email address for this user first.")
		return
	}
	password, err := h.store.ResetUserPassword(r.Context(), id)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if err := h.sendLogin(r, st, u, password); err != nil {
		h.renderUserForm(w, r, sess, u, tr(r,
			"The password was changed to %s, but the email could not be sent: %v", password, err))
		return
	}
	redirectFlash(w, r, "/users/"+strconv.FormatInt(id, 10), tr(r, "A new password was emailed to %s.", u.Email))
}

func (h *Handler) inviteSend(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	inv, err := h.store.Invite(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !inv.Usable() {
		h.renderInvites(w, r, sess, nil, "This invitation can no longer be used.")
		return
	}
	if err := h.sendInvite(r, inv, r.FormValue("email")); err != nil {
		h.renderInvites(w, r, sess, &inv, tr(r, "The invitation could not be emailed: %s", err.Error()))
		return
	}
	redirectFlash(w, r, "/invites", tr(r, "Invitation emailed to %s.", strings.TrimSpace(r.FormValue("email"))))
}

func (h *Handler) sendInvite(r *http.Request, inv store.Invite, to string) error {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		return err
	}
	link := PortalURL(st, r) + "/invite/" + inv.Token
	if err := mail.Send(r.Context(), st.Mail, mail.InviteMessage(st.Language, to, st.PortalName, link)); err != nil {
		h.log.Warn("emailing invitation", "to", to, "err", err)
		return err
	}
	h.log.Info("emailed invitation", "id", inv.ID, "to", strings.TrimSpace(to))
	return nil
}
