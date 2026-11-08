package portal

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/guides"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// The pages in this file help users further: guides to set up their
// player, the devices they own and watch with, and the packages they can
// ask for.

func (h *Handler) selfServiceRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.UserSession))) {
	auth("GET /account/guides/{id}", h.accountGuide)
	auth("GET /account/devices", h.accountDevices)
	auth("POST /account/devices", h.accountDeviceAdd)
	auth("POST /account/devices/{id}/delete", h.accountDeviceDelete)
	auth("GET /account/packages", h.accountPackages)
	auth("POST /account/packages/{id}/request", h.accountPackageRequest)
	auth("POST /account/requests/{id}/cancel", h.accountRequestCancel)
}

func pathInt(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ---- guides ----

// guideCard is a guide in the list on the setup page.
type guideCard struct {
	ID      int64
	Icon    string
	Title   string
	Summary string
}

// guideVars are the details of a user filled in to the guides.
func guideVars(base, portalName, username string) guides.Vars {
	return guides.Vars{
		Portal:   portalName,
		Server:   base,
		Username: username,
		M3U:      base + "/get.php?username=" + username + "&password=YOUR_PASSWORD&type=m3u_plus&output=m3u8",
		EPG:      base + "/xmltv.php?username=" + username + "&password=YOUR_PASSWORD",
	}
}

func (h *Handler) guideCards(r *http.Request) ([]guideCard, error) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		return nil, err
	}
	lang := h.language(r, st)
	list, err := h.store.Guides(r.Context(), true)
	if err != nil {
		return nil, err
	}
	var out []guideCard
	for _, g := range list {
		title, body := g.Localized(lang)
		out = append(out, guideCard{ID: g.ID, Icon: g.Icon, Title: title, Summary: guides.Summary(body)})
	}
	return out, nil
}

type guideData struct {
	Guide  guideCard
	HTML   template.HTML
	Others []guideCard
}

func (h *Handler) accountGuide(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	id, ok := pathInt(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	g, err := h.store.Guide(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !g.Enabled) {
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
	cards, err := h.guideCards(r)
	if err != nil {
		h.serverError(w, err)
		return
	}
	title, body := g.Localized(h.language(r, st))
	d := guideData{Guide: guideCard{ID: g.ID, Icon: g.Icon, Title: title},
		HTML: guides.Render(body, guideVars(web.BaseURL(st.PublicURL, r), st.PortalName, sess.User.Username))}
	for _, c := range cards {
		if c.ID != g.ID {
			d.Others = append(d.Others, c)
		}
	}
	h.renderAccount(w, r, "guide", http.StatusOK, accountPage{Title: title, Nav: "setup", Session: &sess, Data: d})
}

// ---- devices ----

// usedDevice is a player on the devices page.
type usedDevice struct {
	store.UsedDevice
	App      string
	Online   bool
	Watching bool
	Channel  string // what it plays now, when watching
}

type devicesData struct {
	Devices []store.Device
	Used    []usedDevice
	Days    int
	Max     int
	Name    string // form values after an error
	MAC     string
	Limit   int // connection limit, 0 = none
	Now     time.Time
}

// usedDays is how far back the devices page looks for players.
const usedDays = 30

func (h *Handler) devicesData(ctx context.Context, u store.User) (devicesData, error) {
	d := devicesData{Days: usedDays, Max: store.MaxUserDevices, Limit: u.ConnectionLimit(), Now: time.Now()}
	var err error
	if d.Devices, err = h.store.UserDevices(ctx, u.ID); err != nil {
		return d, err
	}
	used, err := h.store.UsedDevices(ctx, u.ID, d.Now.AddDate(0, 0, -usedDays), 50)
	if err != nil {
		return d, err
	}
	var conns []activityConn
	if h.activity != nil {
		for _, c := range h.activity.Snapshot().Connections {
			if c.UserID == u.ID && c.Online(d.Now) {
				conns = append(conns, activityConn{c.IP, c.UserAgent, c.Watching(d.Now), c.Channel, c.LastSeen})
			}
		}
	}
	matched := make([]bool, len(conns))
	for _, ud := range used {
		x := usedDevice{UsedDevice: ud, App: web.PlayerName(ud.UserAgent)}
		for i, c := range conns {
			if c.ip == ud.IP && c.ua == ud.UserAgent {
				matched[i] = true
				x.Online, x.Watching = true, c.watching
				if c.watching {
					x.Channel = c.channel
				}
			}
		}
		d.Used = append(d.Used, x)
	}
	// Players that are online but have not opened anything yet, such as
	// a box that just loaded the channel list.
	for i, c := range conns {
		if !matched[i] {
			x := usedDevice{UsedDevice: store.UsedDevice{IP: c.ip, UserAgent: c.ua, Last: c.seen}, App: web.PlayerName(c.ua),
				Online: true, Watching: c.watching}
			if c.watching {
				x.Channel = c.channel
			}
			d.Used = append([]usedDevice{x}, d.Used...)
		}
	}
	return d, nil
}

type activityConn struct {
	ip, ua   string
	watching bool
	channel  string
	seen     time.Time
}

func (h *Handler) accountDevices(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	h.renderDevices(w, r, sess, http.StatusOK, "", "", "")
}

func (h *Handler) renderDevices(w http.ResponseWriter, r *http.Request, sess store.UserSession, status int, errMsg, name, mac string) {
	d, err := h.devicesData(r.Context(), sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	d.Name, d.MAC = name, mac
	h.renderAccount(w, r, "devices", status, accountPage{Title: "My devices", Nav: "devices", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) accountDeviceAdd(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	name, mac := r.FormValue("name"), r.FormValue("mac")
	if _, err := h.store.AddUserDevice(r.Context(), sess.User.ID, name, mac); err != nil {
		h.renderDevices(w, r, sess, http.StatusBadRequest, err.Error(), name, mac)
		return
	}
	h.log.Info("user registered a device", "username", sess.User.Username)
	redirectFlash(w, r, "/account/devices", "Your device has been added.")
}

func (h *Handler) accountDeviceDelete(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	id, ok := pathInt(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteUserDevice(r.Context(), sess.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/account/devices", "The device has been removed.")
}

// ---- packages ----

// packageCategory is a category of a package with its channels.
type packageCategory struct {
	Name     string
	Radio    bool
	Channels []string
}

// packageOffer is a package as shown on the packages page.
type packageOffer struct {
	store.Package
	TV, Radio  int
	Categories []packageCategory
	// New is how many of its channels the user does not have yet.
	New       int
	Mine      bool
	RequestID int64 // an open request of the user for it
}

type packagesData struct {
	Offers   []packageOffer
	Mine     int // how many of the offers the user has
	Requests []store.PackageRequest
	MailOn   bool
	Message  string // form value after an error
	FormFor  int64
}

func (h *Handler) packagesData(ctx context.Context, u store.User) (packagesData, error) {
	var d packagesData
	pkgs, err := h.store.Packages(ctx)
	if err != nil {
		return d, err
	}
	have, err := h.store.UserChannels(ctx, u.ID)
	if err != nil {
		return d, err
	}
	owned := map[int64]bool{}
	for _, c := range have {
		owned[c.ID] = true
	}
	if d.Requests, err = h.store.UserPackageRequests(ctx, u.ID, 10); err != nil {
		return d, err
	}
	open := map[int64]int64{}
	for _, rq := range d.Requests {
		if rq.Pending() {
			open[rq.PackageID] = rq.ID
		}
	}
	for _, p := range pkgs {
		mine := slices.Contains(u.PackageIDs, p.ID)
		if !p.Public && !mine {
			continue
		}
		o := packageOffer{Package: p, Mine: mine, RequestID: open[p.ID]}
		chs, err := h.store.PackageChannels(ctx, p)
		if err != nil {
			return d, err
		}
		var cat *packageCategory
		for _, c := range chs {
			if c.IsRadio() {
				o.Radio++
			} else {
				o.TV++
			}
			if !owned[c.ID] {
				o.New++
			}
			if cat == nil || cat.Name != c.Category || cat.Radio != c.IsRadio() {
				o.Categories = append(o.Categories, packageCategory{Name: c.Category, Radio: c.IsRadio()})
				cat = &o.Categories[len(o.Categories)-1]
			}
			cat.Channels = append(cat.Channels, c.Name)
		}
		if mine {
			d.Mine++
		}
		d.Offers = append(d.Offers, o)
	}
	// The user's own packages first, then the rest by name.
	slices.SortStableFunc(d.Offers, func(a, b packageOffer) int {
		switch {
		case a.Mine == b.Mine:
			return 0
		case a.Mine:
			return -1
		}
		return 1
	})
	return d, nil
}

func (h *Handler) accountPackages(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	h.renderPackages(w, r, sess, http.StatusOK, "", 0, "")
}

func (h *Handler) renderPackages(w http.ResponseWriter, r *http.Request, sess store.UserSession, status int, errMsg string, formFor int64, message string) {
	d, err := h.packagesData(r.Context(), sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	d.FormFor, d.Message = formFor, message
	h.renderAccount(w, r, "packages", status, accountPage{Title: "Packages", Nav: "packages", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) accountPackageRequest(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	id, ok := pathInt(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	msg := r.FormValue("message")
	rq, err := h.store.RequestPackage(r.Context(), sess.User.ID, id, msg)
	if err != nil {
		h.renderPackages(w, r, sess, http.StatusBadRequest, err.Error(), id, msg)
		return
	}
	h.log.Info("user requested a package", "username", sess.User.Username, "package", rq.Package)
	redirectFlash(w, r, "/account/packages", "Your request has been sent. You will see the answer here.")
}

func (h *Handler) accountRequestCancel(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	id, ok := pathInt(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.CancelPackageRequest(r.Context(), sess.User.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			redirectFlash(w, r, "/account/packages", "This request has already been answered.")
			return
		}
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/account/packages", "Your request has been withdrawn.")
}
