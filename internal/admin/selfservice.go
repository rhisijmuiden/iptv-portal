package admin

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/guides"
	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// The admin side of the account pages: the setup guides users read and
// the packages they ask for.

func (h *Handler) selfServiceRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.Session))) {
	auth("GET /guides", h.guideList)
	auth("GET /guides/new", h.guideForm)
	auth("POST /guides/new", h.guideSave)
	auth("POST /guides/restore", h.guideRestore)
	auth("GET /guides/{id}", h.guideForm)
	auth("POST /guides/{id}", h.guideSave)
	auth("POST /guides/{id}/delete", h.guideDelete)
	auth("GET /requests", h.requestList)
	auth("POST /requests/{id}", h.requestDecide)
	auth("POST /users/{id}/devices", h.userDeviceAdd)
	auth("POST /users/{id}/devices/{device}/delete", h.userDeviceDelete)
}

// ---- guides ----

type guideListData struct {
	Guides   []store.Guide
	Missing  int  // default guides that were removed
	Accounts bool // the account pages are on
}

func (h *Handler) guideList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	list, err := h.store.Guides(r.Context(), false)
	if err != nil {
		h.serverError(w, err)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	d := guideListData{Guides: list, Accounts: st.AccountPortal}
	have := map[string]bool{}
	for _, g := range list {
		have[g.Builtin] = true
	}
	for _, def := range guides.Defaults {
		if !have[def.Key] {
			d.Missing++
		}
	}
	h.render(w, r, "guides", page{Title: "Setup guides", Nav: "guides", Session: &sess, Data: d})
}

type guideFormData struct {
	Guide        store.Guide
	Icons        []string
	Placeholders []string
	Preview      template.HTML
	PreviewNL    template.HTML
}

func (h *Handler) guideForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	g := store.Guide{Enabled: true, Icon: "tv"}
	if id, ok := pathID(r); ok {
		var err error
		if g, err = h.store.Guide(r.Context(), id); errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			h.serverError(w, err)
			return
		}
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	} else {
		list, err := h.store.Guides(r.Context(), false)
		if err != nil {
			h.serverError(w, err)
			return
		}
		for _, x := range list {
			g.SortOrder = max(g.SortOrder, x.SortOrder+10)
		}
	}
	h.renderGuideForm(w, r, sess, g, "")
}

func (h *Handler) renderGuideForm(w http.ResponseWriter, r *http.Request, sess store.Session, g store.Guide, errMsg string) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	base := PortalURL(st, r)
	// The preview fills in an example user.
	vars := guides.Vars{Portal: st.PortalName, Server: base, Username: "username",
		M3U: base + "/get.php?username=username&password=YOUR_PASSWORD&type=m3u_plus&output=m3u8",
		EPG: base + "/xmltv.php?username=username&password=YOUR_PASSWORD"}
	d := guideFormData{Guide: g, Icons: guides.Icons, Placeholders: guides.Placeholders, Preview: guides.Render(g.Body, vars)}
	if strings.TrimSpace(g.BodyNL) != "" {
		d.PreviewNL = guides.Render(g.BodyNL, vars)
	}
	title := "Add guide"
	if g.ID != 0 {
		title = "Edit guide"
	}
	h.render(w, r, "guide", page{Title: title, Nav: "guides", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) guideSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	order, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
	g := store.Guide{
		Icon: r.FormValue("icon"), Title: r.FormValue("title"), Body: r.FormValue("body"),
		TitleNL: r.FormValue("title_nl"), BodyNL: r.FormValue("body_nl"),
		SortOrder: order, Enabled: r.FormValue("enabled") == "on",
	}
	if id, ok := pathID(r); ok {
		g.ID = id
		if old, err := h.store.Guide(r.Context(), id); err == nil {
			g.Builtin = old.Builtin
		}
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	if _, err := h.store.SaveGuide(r.Context(), g); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.renderGuideForm(w, r, sess, g, err.Error())
		return
	}
	redirectFlash(w, r, "/guides", tr(r, "Guide %q saved.", strings.TrimSpace(g.Title)))
}

func (h *Handler) guideDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteGuide(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/guides", "Guide deleted.")
}

func (h *Handler) guideRestore(w http.ResponseWriter, r *http.Request, sess store.Session) {
	n, err := h.store.RestoreGuides(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	if n == 1 {
		redirectFlash(w, r, "/guides", "1 standard guide added back.")
		return
	}
	redirectFlash(w, r, "/guides", tr(r, "%d standard guides added back.", n))
}

// ---- package requests ----

type requestListData struct {
	Open    []store.PackageRequest
	Decided []store.PackageRequest
	MailOn  bool
	Now     time.Time
}

func (h *Handler) requestList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	open, decided, err := h.store.PackageRequests(r.Context(), 50)
	if err != nil {
		h.serverError(w, err)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "requests", page{Title: "Package requests", Nav: "requests", Session: &sess,
		Data: requestListData{Open: open, Decided: decided, MailOn: st.Mail.Configured(), Now: time.Now()}})
}

func (h *Handler) requestDecide(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	action := r.FormValue("action")
	if action != "add" && action != "replace" && action != "reject" {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	rq, err := h.store.DecidePackageRequest(r.Context(), id, action != "reject", action == "replace", r.FormValue("reply"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		redirectError(w, r, "/requests", err.Error())
		return
	}
	h.log.Info("package request answered", "username", rq.Username, "package", rq.Package, "status", rq.Status, "admin", sess.Admin.Username)
	h.mailRequestAnswer(r, rq)
	if rq.Status == store.RequestApproved {
		redirectFlash(w, r, "/requests", tr(r, "%s now has the package %s.", rq.Username, rq.Package))
		return
	}
	redirectFlash(w, r, "/requests", tr(r, "The request of %s has been rejected.", rq.Username))
}

// mailRequestAnswer emails the user the answer to their request, when
// they have an email address and a mail server is set up.
func (h *Handler) mailRequestAnswer(r *http.Request, rq store.PackageRequest) {
	st, err := h.store.Settings(r.Context())
	if err != nil || rq.Email == "" || !st.Mail.Configured() {
		return
	}
	lang := st.Language
	if lang == "" {
		lang = i18n.English
	}
	msg := mail.PackageAnswerMessage(lang, rq.Email, st.PortalName, rq.Username, rq.Package,
		rq.Status == store.RequestApproved, rq.Reply, strings.TrimSuffix(PortalURL(st, r), "/")+"/account/packages")
	go func() {
		if err := mail.Send(context.Background(), st.Mail, msg); err != nil {
			h.log.Warn("emailing package request answer", "username", rq.Username, "err", err)
		}
	}()
}

// userDeviceAdd registers a device (MAC address) for a user, for example
// the MAG box the admin hands out.
func (h *Handler) userDeviceAdd(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := h.store.User(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.serverError(w, err)
		return
	}
	back := "/users/" + strconv.FormatInt(id, 10)
	if _, err := h.store.AddUserDevice(r.Context(), id, r.FormValue("name"), r.FormValue("mac")); err != nil {
		redirectError(w, r, back, err.Error())
		return
	}
	redirectFlash(w, r, back, "The device has been added.")
}

func (h *Handler) userDeviceDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	device, err := strconv.ParseInt(r.PathValue("device"), 10, 64)
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteUserDevice(r.Context(), id, device); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/users/"+strconv.FormatInt(id, 10), "The device has been removed.")
}
