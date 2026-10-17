package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// formIDs returns the positive integer values of a multi-value form field.
func formIDs(r *http.Request, name string) []int64 {
	var out []int64
	for _, v := range r.Form[name] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// formDate parses a yyyy-mm-dd date field as the end of that day in the
// server's time zone. An empty field returns nil.
func formDate(r *http.Request, name string) (*time.Time, error) {
	v := strings.TrimSpace(r.FormValue(name))
	if v == "" {
		return nil, nil
	}
	d, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil, errors.New("invalid date")
	}
	end := d.Add(24*time.Hour - time.Second)
	return &end, nil
}

// ---- users ----

type userListData struct {
	Users  []store.User
	Search string
	Back   string
}

func (h *Handler) userList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	q := r.URL.Query().Get("q")
	users, err := h.store.Users(r.Context(), q)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "users", page{Title: "Users", Nav: "users", Session: &sess,
		Data: userListData{Users: users, Search: q, Back: r.URL.RequestURI()}})
}

type userFormData struct {
	User      store.User
	Packages  []store.Package
	PortalURL string
	MailOn    bool // a mail server is configured
	SendLogin bool // the "email the login details" box is ticked
}

func (h *Handler) userForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	u := store.User{Enabled: true}
	if id, ok := pathID(r); ok {
		var err error
		u, err = h.store.User(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	h.renderUserForm(w, r, sess, u, "")
}

func (h *Handler) renderUserForm(w http.ResponseWriter, r *http.Request, sess store.Session, u store.User, errMsg string) {
	h.renderUserFormData(w, r, sess, userFormData{User: u}, errMsg)
}

func (h *Handler) renderUserFormData(w http.ResponseWriter, r *http.Request, sess store.Session, d userFormData, errMsg string) {
	pkgs, err := h.store.Packages(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	title := "Add user"
	if d.User.ID != 0 {
		title = "Edit user"
	}
	d.Packages, d.PortalURL, d.MailOn = pkgs, PortalURL(st, r), st.Mail.Configured()
	h.render(w, r, "user", page{Title: title, Nav: "users", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) userSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	u := store.User{
		Username:   r.FormValue("username"),
		Note:       r.FormValue("note"),
		Email:      r.FormValue("email"),
		Enabled:    r.FormValue("enabled") == "on",
		PackageIDs: formIDs(r, "package"),
	}
	if id, ok := pathID(r); ok {
		u.ID = id
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	password := r.FormValue("password")
	send := r.FormValue("send_login") == "on"
	fail := func(msg string) {
		h.renderUserFormData(w, r, sess, userFormData{User: u, SendLogin: send}, msg)
	}
	exp, err := formDate(r, "expires")
	if err != nil {
		fail(err.Error())
		return
	}
	u.ExpiresAt = exp
	if send && strings.TrimSpace(u.Email) == "" {
		fail("Enter an email address to send the login details to.")
		return
	}
	if send && password == "" {
		fail("Enter a new password to email it, or use \"Email new password\" to generate one.")
		return
	}
	id, err := h.store.SaveUser(r.Context(), u, password)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		fail(err.Error())
		return
	}
	msg := fmt.Sprintf("User %q saved.", strings.TrimSpace(u.Username))
	if send {
		saved, err := h.store.User(r.Context(), id)
		if err != nil {
			h.serverError(w, err)
			return
		}
		st, err := h.store.Settings(r.Context())
		if err != nil {
			h.serverError(w, err)
			return
		}
		if err := h.sendLogin(r, st, saved, password); err != nil {
			h.renderUserForm(w, r, sess, saved, "The user was saved, but the login details could not be emailed: "+err.Error())
			return
		}
		msg = fmt.Sprintf("User %q saved and the login details emailed to %s.", saved.Username, saved.Email)
	}
	redirectFlash(w, r, "/users", msg)
}

func (h *Handler) userToggle(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.SetUserEnabled(r.Context(), id, r.FormValue("enabled") == "1"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.serverError(w, err)
		return
	}
	http.Redirect(w, r, backTo(r, "/users"), http.StatusSeeOther)
}

func (h *Handler) userDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteUser(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/users", "User deleted.")
}

// ---- packages ----

type packageListData struct {
	Packages   []store.Package
	Categories map[int64]string
}

func (h *Handler) packageList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	pkgs, err := h.store.Packages(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	cats, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	names := map[int64]string{}
	for _, c := range cats {
		names[c.ID] = c.Name
	}
	h.render(w, r, "packages", page{Title: "Packages", Nav: "packages", Session: &sess,
		Data: packageListData{Packages: pkgs, Categories: names}})
}

type packageFormData struct {
	Package    store.Package
	Categories []store.Category
}

func (h *Handler) packageForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var p store.Package
	if id, ok := pathID(r); ok {
		var err error
		p, err = h.store.Package(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	h.renderPackageForm(w, r, sess, p, "")
}

func (h *Handler) renderPackageForm(w http.ResponseWriter, r *http.Request, sess store.Session, p store.Package, errMsg string) {
	cats, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	title := "Add package"
	if p.ID != 0 {
		title = "Edit package"
	}
	h.render(w, r, "package", page{Title: title, Nav: "packages", Session: &sess, Error: errMsg,
		Data: packageFormData{Package: p, Categories: cats}})
}

func (h *Handler) packageSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	p := store.Package{
		Name:        r.FormValue("name"),
		AllChannels: r.FormValue("all_channels") == "on",
		CategoryIDs: formIDs(r, "category"),
	}
	if id, ok := pathID(r); ok {
		p.ID = id
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	if _, err := h.store.SavePackage(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.renderPackageForm(w, r, sess, p, err.Error())
		return
	}
	redirectFlash(w, r, "/packages", fmt.Sprintf("Package %q saved.", strings.TrimSpace(p.Name)))
}

func (h *Handler) packageDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeletePackage(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/packages", "Package deleted.")
}

// ---- invites ----

type inviteListData struct {
	Invites   []store.Invite
	Packages  []store.Package
	PortalURL string
	New       *store.Invite // the invite that was just created
	MailOn    bool          // a mail server is configured
}

func (h *Handler) inviteList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.renderInvites(w, r, sess, nil, "")
}

func (h *Handler) renderInvites(w http.ResponseWriter, r *http.Request, sess store.Session, created *store.Invite, errMsg string) {
	invs, err := h.store.Invites(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	pkgs, err := h.store.Packages(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "invites", page{Title: "Invitations", Nav: "invites", Session: &sess, Error: errMsg,
		Data: inviteListData{Invites: invs, Packages: pkgs, PortalURL: PortalURL(st, r), New: created, MailOn: st.Mail.Configured()}})
}

func (h *Handler) inviteCreate(w http.ResponseWriter, r *http.Request, sess store.Session) {
	inv := store.Invite{Note: r.FormValue("note"), PackageIDs: formIDs(r, "package")}
	var err error
	if inv.MaxUses, err = strconv.Atoi(r.FormValue("max_uses")); err != nil {
		h.renderInvites(w, r, sess, nil, "number of uses must be a number")
		return
	}
	if v := strings.TrimSpace(r.FormValue("valid_days")); v != "" {
		if inv.ValidDays, err = strconv.Atoi(v); err != nil {
			h.renderInvites(w, r, sess, nil, "account validity must be a number of days")
			return
		}
	}
	if inv.ExpiresAt, err = formDate(r, "expires"); err != nil {
		h.renderInvites(w, r, sess, nil, err.Error())
		return
	}
	to := strings.TrimSpace(r.FormValue("email"))
	if to != "" {
		if _, err := mail.ParseAddress(to); err != nil {
			h.renderInvites(w, r, sess, nil, "email address is not valid")
			return
		}
	}
	created, err := h.store.CreateInvite(r.Context(), inv)
	if err != nil {
		h.renderInvites(w, r, sess, nil, err.Error())
		return
	}
	if to == "" {
		h.renderInvites(w, r, sess, &created, "")
		return
	}
	if err := h.sendInvite(r, created, to); err != nil {
		h.renderInvites(w, r, sess, &created, "The invitation was created, but could not be emailed: "+err.Error())
		return
	}
	redirectFlash(w, r, "/invites", fmt.Sprintf("Invitation created and emailed to %s.", to))
}

func (h *Handler) inviteRevoke(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.RevokeInvite(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/invites", "Invitation revoked.")
}

func (h *Handler) inviteDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteInvite(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/invites", "Invitation deleted.")
}
