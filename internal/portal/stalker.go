package portal

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// This file implements the middleware API of Stalker / Ministra portals,
// which MAG boxes and apps that emulate them (MyTVOnline "MAC portal",
// STB Emulator, Smart STB, Kodi's Stalker client, ...) use instead of an
// Xtream Codes login. Such a player logs in with the MAC address of the
// device only, so it works for MAC addresses that users (or an admin)
// registered with an account, and only when the admin turned it on.
//
// Every request goes to load.php (or portal.php) with type= and action=
// and gets {"js": ...} back. Streams are played through short links on the
// portal (/stalker/play/...), which redirect live channels to the
// broadcaster like /live/... does and play On Demand like /movie/... does.

// stalkerPageSize is the number of items on a page of a Stalker list.
const stalkerPageSize = 14

// stalkerLinkTTL is how long a play link handed to a player works.
const stalkerLinkTTL = 7 * 24 * time.Hour

// stalkerSecretKey is the stored key that signs the play links.
const stalkerSecretKey = "stalker_link_secret"

// stalkerRoutes registers the paths players use for the portal. The
// address entered in a player is <portal>/c/ (or /stalker_portal/c/);
// players then call load.php next to it, or portal.php.
func (h *Handler) stalkerRoutes() {
	for _, p := range []string{"/portal.php", "/server/load.php", "/c/portal.php", "/c/server/load.php",
		"/stalker_portal/portal.php", "/stalker_portal/server/load.php", "/stalker_portal/c/portal.php"} {
		h.mux.HandleFunc(p, h.stalkerAPI)
	}
	for _, p := range []string{"/c/", "/stalker_portal/c/"} {
		h.mux.HandleFunc("GET "+p+"{$}", h.stalkerIndex)
		h.mux.HandleFunc("GET "+p+"version.js", h.stalkerVersion)
		h.mux.HandleFunc("GET "+p+"xpcom.common.js", h.stalkerCommonJS)
	}
	h.mux.HandleFunc("GET /stalker/play/{token}/{file}", h.stalkerPlay)
}

// stalkerOn checks that the Stalker portal is turned on; otherwise it
// answers 404, as if the portal did not have it.
func (h *Handler) stalkerOn(w http.ResponseWriter, r *http.Request) (store.Settings, bool) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return st, false
	}
	if !st.StalkerPortal {
		http.NotFound(w, r)
		return st, false
	}
	return st, true
}

// stalkerIndex is the page a MAG box opens at the portal address. The
// portal has no on-screen menu of its own for MAG boxes, so it explains
// which players can use it.
func (h *Handler) stalkerIndex(w http.ResponseWriter, r *http.Request) {
	st, ok := h.stalkerOn(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "%s\n\n%s\n", st.PortalName,
		i18n.T(st.Language, "This is a Stalker portal address. Enter it in an app or TV box that supports MAC portals, and register the MAC address of the device with your account."))
}

func (h *Handler) stalkerVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.stalkerOn(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	w.Write([]byte("var ver = '5.6.1';\n"))
}

// stalkerCommonJS is read by players (Kodi's Stalker client, among others)
// to find out where load.php is: they look for the ajax_loader line.
func (h *Handler) stalkerCommonJS(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.stalkerOn(w, r); !ok {
		return
	}
	dir := strings.TrimSuffix(strings.TrimSuffix(r.URL.Path, "xpcom.common.js"), "/")
	dir = strings.Trim(strings.TrimSuffix(dir, "/c"), "/")
	w.Header().Set("Content-Type", "application/javascript")
	loader := "/server/load.php"
	if dir != "" {
		loader = "/\"+this.portal_path+\"/server/load.php"
	}
	fmt.Fprintf(w, "this.portal_path = %q;\nthis.ajax_loader = this.portal_protocol+\"://\"+this.portal_ip+\"%s\";\n", dir, loader)
}

// stalkerMAC is the MAC address a player sends: a "mac" cookie, or a
// mac parameter.
func stalkerMAC(r *http.Request) string {
	raw := ""
	if c, err := r.Cookie("mac"); err == nil {
		raw = c.Value
	}
	if raw == "" {
		raw = r.FormValue("mac")
	}
	if v, err := url.QueryUnescape(raw); err == nil {
		raw = v
	}
	mac, err := store.NormalizeMAC(raw)
	if err != nil {
		return ""
	}
	return mac
}

// stalkerJSON writes the {"js": ...} answer of load.php.
func stalkerJSON(w http.ResponseWriter, v any) {
	writeJSON(w, map[string]any{"js": v})
}

// stalkerRequest is one authenticated load.php request.
type stalkerRequest struct {
	st     store.Settings
	base   string
	device store.Device
	user   store.User
	mac    string
}

func (h *Handler) stalkerAPI(w http.ResponseWriter, r *http.Request) {
	st, ok := h.stalkerOn(w, r)
	if !ok {
		return
	}
	typ, action := r.FormValue("type"), r.FormValue("action")
	ip := web.ClientIP(r)
	if typ == "stb" && action == "handshake" {
		// Handing out a token proves nothing, so it needs no MAC address;
		// players send it back with every request.
		token := r.FormValue("token")
		if len(token) < 16 || len(token) > 64 {
			token = randomHex(16)
		}
		stalkerJSON(w, map[string]any{"token": strings.ToUpper(token), "random": randomHex(20)})
		return
	}
	if !h.authLimiter.Allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		stalkerJSON(w, map[string]any{"error": "too many failed logins, try again later"})
		return
	}
	mac := stalkerMAC(r)
	if typ == "stb" && action == "do_auth" {
		h.stalkerLogin(w, r, mac)
		return
	}
	dev, u, err := h.store.AuthenticateDevice(r.Context(), mac)
	switch {
	case errors.Is(err, store.ErrNotFound):
		h.authLimiter.Fail(ip)
		if typ == "stb" && action == "get_profile" {
			// Status 2 makes the player ask for a login, which links the
			// device to that account (do_auth).
			stalkerJSON(w, stalkerProfile(st, mac, store.User{}, 2,
				i18n.T(st.Language, "This device is not registered. Add its MAC address on your account page, or log in with your username and password.")))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		stalkerJSON(w, map[string]any{"error": "unknown device"})
		return
	case errors.Is(err, store.ErrUserInactive):
		if typ == "stb" && action == "get_profile" {
			stalkerJSON(w, stalkerProfile(st, mac, u, 1, i18n.T(st.Language, "Your account is disabled or expired.")))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		stalkerJSON(w, map[string]any{"error": "account is disabled or expired"})
		return
	case err != nil:
		h.serverError(w, err)
		return
	}
	h.authLimiter.Reset(ip)
	h.seen(r, u, ip)
	sr := stalkerRequest{st: st, base: web.BaseURL(st.PublicURL, r), device: dev, user: u, mac: mac}

	switch typ + "/" + action {
	case "stb/get_profile":
		stalkerJSON(w, stalkerProfile(st, mac, u, 0, ""))
	case "account_info/get_main_info":
		h.stalkerAccountInfo(w, sr)
	case "watchdog/get_events":
		stalkerJSON(w, map[string]any{"data": map[string]any{"msgs": 0, "additional_services_on": "1"}})
	case "itv/get_genres":
		h.stalkerGenres(w, r, sr, store.KindTV)
	case "radio/get_genres":
		h.stalkerGenres(w, r, sr, store.KindRadio)
	case "itv/get_all_channels", "itv/get_ordered_list":
		h.stalkerChannels(w, r, sr, store.KindTV, action == "get_all_channels")
	case "radio/get_all_channels", "radio/get_ordered_list":
		h.stalkerChannels(w, r, sr, store.KindRadio, action == "get_all_channels")
	case "itv/get_all_fav_channels", "radio/get_all_fav_channels":
		stalkerJSON(w, stalkerPage(nil, 1, 0))
	case "itv/create_link", "radio/create_link":
		h.stalkerChannelLink(w, r, sr)
	case "itv/get_short_epg":
		h.stalkerShortEPG(w, r, sr)
	case "itv/get_epg_info":
		h.stalkerEPGInfo(w, r, sr)
	case "epg/get_simple_data_table", "epg/get_data_table":
		h.stalkerDayEPG(w, r, sr)
	case "epg/get_week":
		stalkerJSON(w, stalkerWeek(time.Now()))
	case "vod/get_categories", "series/get_categories":
		h.stalkerVODCategories(w, r, sr)
	case "vod/get_ordered_list", "series/get_ordered_list":
		h.stalkerVODList(w, r, sr)
	case "vod/create_link", "series/create_link":
		h.stalkerVODLink(w, r, sr)
	case "vod/get_genres_by_category_alias", "vod/get_years", "vod/get_abc",
		"itv/get_fav_ids", "radio/get_fav_ids", "stb/get_preload_images", "stb/get_modules":
		stalkerJSON(w, []any{})
	case "stb/get_localization", "stb/get_settings_profile":
		stalkerJSON(w, map[string]any{})
	default:
		// set_fav, set_last_id, log, set_modules, ... are accepted and
		// forgotten.
		stalkerJSON(w, true)
	}
}

// stalkerLogin links a device to the account the player logs in with
// (do_auth), so the next time the MAC address alone is enough.
func (h *Handler) stalkerLogin(w http.ResponseWriter, r *http.Request, mac string) {
	ip := web.ClientIP(r)
	if mac == "" {
		stalkerJSON(w, false)
		return
	}
	u, err := h.store.AuthenticateUser(r.Context(), r.FormValue("login"), r.FormValue("password"))
	switch {
	case errors.Is(err, store.ErrInvalidLogin):
		h.authLimiter.Fail(ip)
		stalkerJSON(w, false)
		return
	case errors.Is(err, store.ErrUserInactive):
		stalkerJSON(w, false)
		return
	case err != nil:
		h.serverError(w, err)
		return
	}
	h.authLimiter.Reset(ip)
	devs, err := h.store.UserDevices(r.Context(), u.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	for _, d := range devs {
		if d.MAC == mac {
			stalkerJSON(w, true)
			return
		}
	}
	if _, err := h.store.AddUserDevice(r.Context(), u.ID, "MAG "+mac[9:], mac); err != nil {
		h.log.Info("linking device refused", "username", u.Username, "mac", mac, "err", err)
		stalkerJSON(w, false)
		return
	}
	h.log.Info("device linked by logging in on the Stalker portal", "username", u.Username, "mac", mac, "ip", ip)
	stalkerJSON(w, true)
}

// stalkerProfile is the get_profile answer. status 0 is a working account,
// 1 a blocked one (msg says why) and 2 an unknown device that has to log in.
func stalkerProfile(st store.Settings, mac string, u store.User, status int, msg string) map[string]any {
	locale := "en_GB.utf8"
	if st.Language == i18n.Dutch {
		locale = "nl_NL.utf8"
	}
	blocked := "0"
	if status == 1 {
		blocked = "1"
	}
	return map[string]any{
		"id":                                    strconv.FormatInt(u.ID, 10),
		"name":                                  u.Username,
		"login":                                 u.Username,
		"fname":                                 u.Username,
		"mac":                                   mac,
		"ls":                                    "",
		"status":                                status,
		"blocked":                               blocked,
		"msg":                                   msg,
		"stb_lang":                              st.Language,
		"locale":                                locale,
		"default_locale":                        locale,
		"default_timezone":                      timezoneName(),
		"timezone_diff":                         0,
		"expire_billing_date":                   stalkerExpiry(u),
		"tariff_expired_date":                   nil,
		"parent_password":                       "0000",
		"settings_password":                     "0000",
		"account_balance":                       "",
		"aspect":                                "16",
		"volume":                                "100",
		"audio_out":                             "1",
		"playback_buffer_bytes":                 "0",
		"playback_buffer_size":                  "0",
		"playback_limit":                        0,
		"hd":                                    "1",
		"fav_itv_on":                            "0",
		"last_itv_id":                           0,
		"watchdog_timeout":                      120,
		"timeslot":                              40,
		"timeslot_ratio":                        1,
		"storages":                              map[string]any{},
		"allowed_stb_types":                     []string{"mag200", "mag245", "mag250", "mag254", "mag256", "mag322", "mag324", "mag349", "mag351", "mag420", "mag424", "mag520", "mag524", "aurahd", "wr320"},
		"allowed_stb_types_for_local_recording": []string{},
		"strict_stb_type_check":                 "",
		"enable_tariff_plans":                   "",
		"enable_buffering_indication":           "1",
		"show_tv_channel_logo":                  true,
		"play_in_preview_by_ok":                 nil,
		"tv_archive_continued":                  "",
		"tv_quality_filter":                     "",
		"updated":                               map[string]any{"id": "1", "uid": strconv.FormatInt(u.ID, 10), "anec": "0", "vclub": "0"},
		"additional_services_on":                "1",
	}
}

// stalkerExpiry is the expiry date of an account as Stalker writes it.
func stalkerExpiry(u store.User) string {
	if u.ExpiresAt == nil {
		return "0000-00-00 00:00:00"
	}
	return u.ExpiresAt.Local().Format("2006-01-02 15:04:05")
}

func (h *Handler) stalkerAccountInfo(w http.ResponseWriter, sr stalkerRequest) {
	end := i18n.T(sr.st.Language, "Unlimited")
	if sr.user.ExpiresAt != nil {
		end = i18n.LongDate(sr.st.Language, sr.user.ExpiresAt.Local())
	}
	// Players show "phone" as the end of the subscription.
	stalkerJSON(w, map[string]any{"mac": sr.mac, "fname": sr.user.Username, "login": sr.user.Username,
		"phone": end, "end_date": end, "ls": "", "tariff_plan": strings.Join(sr.user.Packages, ", ")})
}

// stalkerPage is one page of a Stalker list. page counts from 1.
func stalkerPage(items []map[string]any, page, selected int) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	total := len(items)
	if page < 1 {
		page = 1
	}
	from := min((page-1)*stalkerPageSize, total)
	to := min(from+stalkerPageSize, total)
	return map[string]any{"total_items": total, "max_page_items": stalkerPageSize, "selected_item": selected,
		"cur_page": page, "data": items[from:to]}
}

// stalkerPageNumber reads the p parameter: 0 or nothing is the first page.
func stalkerPageNumber(r *http.Request) int {
	p, _ := strconv.Atoi(r.FormValue("p"))
	return max(p, 1)
}

// ---- live TV and radio ----

// kindChannels returns the channels of one kind (TV or radio) the user's
// player lists, in player order.
func (h *Handler) kindChannels(ctx context.Context, u store.User, kind string) ([]store.Channel, error) {
	// The user's own order of groups, without hidden groups and groups
	// locked by parental controls, as Xtream players get them.
	v, _, err := h.playerView(ctx, u)
	if err != nil {
		return nil, err
	}
	chs := slices.Clone(v.Channels)
	return slices.DeleteFunc(chs, func(c store.Channel) bool { return c.IsRadio() != (kind == store.KindRadio) }), nil
}

func (h *Handler) stalkerGenres(w http.ResponseWriter, r *http.Request, sr stalkerRequest, kind string) {
	chs, err := h.kindChannels(r.Context(), sr.user, kind)
	if err != nil {
		h.serverError(w, err)
		return
	}
	all := "All channels"
	if kind == store.KindRadio {
		all = "All stations"
	}
	out := []map[string]any{{"id": "*", "title": i18n.T(sr.st.Language, all), "alias": "*", "censored": 0}}
	seen := map[string]bool{}
	for _, c := range chs {
		id := categoryID(c)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, map[string]any{"id": id, "title": categoryName(c, false), "alias": strings.ToLower(categoryName(c, false)),
			"censored": 0, "number": len(out)})
	}
	stalkerJSON(w, out)
}

// stalkerChannelCmd is the cmd of a channel in a list. TV players turn it
// into a play link with create_link (use_http_tmp_link); radio players
// play the cmd itself, so radio gets its play link right away.
func (h *Handler) stalkerChannelCmd(ctx context.Context, sr stalkerRequest, c store.Channel) string {
	if c.IsRadio() {
		return "ffmpeg " + h.stalkerLink(ctx, sr, "ch", c.ID, streamExt(c.StreamURL))
	}
	return "ffrt http://localhost/ch/" + strconv.FormatInt(c.ID, 10)
}

func (h *Handler) stalkerChannels(w http.ResponseWriter, r *http.Request, sr stalkerRequest, kind string, all bool) {
	chs, err := h.kindChannels(r.Context(), sr.user, kind)
	if err != nil {
		h.serverError(w, err)
		return
	}
	genre := r.FormValue("genre")
	search := strings.ToLower(strings.TrimSpace(r.FormValue("search")))
	var items []map[string]any
	for i, c := range chs {
		if !all && genre != "" && genre != "*" && genre != categoryID(c) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(c.Name), search) {
			continue
		}
		item := map[string]any{
			"id":                    strconv.FormatInt(c.ID, 10),
			"name":                  c.Name,
			"number":                strconv.Itoa(i + 1),
			"cmd":                   h.stalkerChannelCmd(r.Context(), sr, c),
			"logo":                  logoURL(sr.base, c),
			"xmltv_id":              epgID(c),
			"tv_genre_id":           categoryID(c),
			"genre_id":              categoryID(c),
			"use_http_tmp_link":     "1",
			"status":                1,
			"open":                  1,
			"censored":              0,
			"hd":                    0,
			"fav":                   0,
			"lock":                  0,
			"archive":               0,
			"tv_archive_duration":   0,
			"enable_tv_archive":     0,
			"allow_pvr":             0,
			"allow_local_pvr":       0,
			"allow_local_timeshift": "0",
			"mc_cmd":                "",
			"epg":                   []any{},
		}
		if c.IsRadio() {
			item["radio"] = true
			item["use_http_tmp_link"] = "0"
		}
		items = append(items, item)
	}
	if all {
		// get_all_channels returns the whole list at once.
		if items == nil {
			items = []map[string]any{}
		}
		stalkerJSON(w, map[string]any{"total_items": len(items), "max_page_items": len(items), "selected_item": 0,
			"cur_page": 0, "data": items})
		return
	}
	stalkerJSON(w, stalkerPage(items, stalkerPageNumber(r), 0))
}

var (
	channelCmd = regexp.MustCompile(`/ch/(\d+)`)
	movieCmd   = regexp.MustCompile(`/vod/(\d+)`)
	seasonCmd  = regexp.MustCompile(`/series/(\d+)/(\d+)`)
	linkCmd    = regexp.MustCompile(`/stalker/play/([^/\s]+)/`)
)

// stalkerChannelLink answers create_link for TV and radio.
func (h *Handler) stalkerChannelLink(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	cmd := r.FormValue("cmd")
	var id int64
	if m := channelCmd.FindStringSubmatch(cmd); m != nil {
		id, _ = strconv.ParseInt(m[1], 10, 64)
	} else if m := linkCmd.FindStringSubmatch(cmd); m != nil {
		// A radio station's cmd is already a play link.
		if l, ok := h.verifyLink(r.Context(), m[1]); ok && l.kind == "ch" {
			id = l.id
		}
	}
	c, ok, err := h.userChannel(r, sr.user, id)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !ok {
		stalkerJSON(w, map[string]any{"id": id, "cmd": "", "error": "nothing_to_play"})
		return
	}
	stalkerJSON(w, map[string]any{"id": strconv.FormatInt(c.ID, 10), "cmd": "ffmpeg " + h.stalkerLink(r.Context(), sr, "ch", c.ID, streamExt(c.StreamURL)),
		"streamer_id": 0, "link_id": 0, "load": 0, "error": ""})
}

// ---- guide ----

// channelProgrammes returns the guide of a channel between from and until.
// wait reads the current song of a radio station when it is not cached.
func (h *Handler) channelProgrammes(ctx context.Context, st store.Settings, c store.Channel, from, until time.Time, wait bool) []epg.Programme {
	if epgID(c) == "" {
		return nil
	}
	if !h.radioGuide(c) {
		return h.guide.Programmes(c.EPGID, from, until)
	}
	var out []epg.Programme
	for _, p := range radioProgrammes(st.Language, c, h.currentSong(ctx, st, c, wait), time.Now()) {
		if p.Stop.After(from) && p.Start.Before(until) {
			out = append(out, p)
		}
	}
	return out
}

func stalkerProgramme(c store.Channel, p epg.Programme, now time.Time) map[string]any {
	start, stop := p.Start.Local(), p.Stop.Local()
	open := 0
	if !p.Start.After(now) && p.Stop.After(now) {
		open = 1
	}
	return map[string]any{
		"id":              strconv.FormatInt(c.ID, 10) + strconv.FormatInt(p.Start.Unix(), 10),
		"ch_id":           strconv.FormatInt(c.ID, 10),
		"real_id":         strconv.FormatInt(c.ID, 10) + "_" + strconv.FormatInt(p.Start.Unix(), 10),
		"time":            start.Format("2006-01-02 15:04:05"),
		"time_to":         stop.Format("2006-01-02 15:04:05"),
		"duration":        int(p.Stop.Sub(p.Start).Seconds()),
		"name":            p.Title,
		"descr":           p.Desc,
		"category":        "",
		"director":        "",
		"actor":           "",
		"start_timestamp": p.Start.Unix(),
		"stop_timestamp":  p.Stop.Unix(),
		"t_time":          start.Format("15:04"),
		"t_time_to":       stop.Format("15:04"),
		"open":            open,
		"mark_memo":       0,
		"mark_archive":    0,
		"mark_rec":        0,
	}
}

// stalkerChannelParam returns the channel of the ch_id parameter; found
// is false when the user has no such channel. On an error it writes the
// response and returns ok false.
func (h *Handler) stalkerChannelParam(w http.ResponseWriter, r *http.Request, sr stalkerRequest) (c store.Channel, found, ok bool) {
	id, _ := strconv.ParseInt(r.FormValue("ch_id"), 10, 64)
	c, found, err := h.userChannel(r, sr.user, id)
	if err != nil {
		h.serverError(w, err)
		return c, false, false
	}
	return c, found, true
}

func (h *Handler) stalkerShortEPG(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	out := []map[string]any{}
	c, found, ok := h.stalkerChannelParam(w, r, sr)
	if !ok {
		return
	}
	if !found {
		stalkerJSON(w, out)
		return
	}
	size, _ := strconv.Atoi(r.FormValue("size"))
	if size <= 0 {
		size = 10
	}
	now := time.Now()
	for _, p := range h.channelProgrammes(r.Context(), sr.st, c, now, now.Add(7*24*time.Hour), true) {
		if len(out) >= size {
			break
		}
		out = append(out, stalkerProgramme(c, p, now))
	}
	stalkerJSON(w, out)
}

// stalkerEPGInfo answers get_epg_info: the guide of all channels for the
// next period hours.
func (h *Handler) stalkerEPGInfo(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	period, _ := strconv.Atoi(r.FormValue("period"))
	if period <= 0 || period > 7*24 {
		period = 3
	}
	chs, err := h.kindChannels(r.Context(), sr.user, store.KindTV)
	if err != nil {
		h.serverError(w, err)
		return
	}
	now := time.Now()
	data := map[string]any{}
	for _, c := range chs {
		progs := h.channelProgrammes(r.Context(), sr.st, c, now, now.Add(time.Duration(period)*time.Hour), false)
		if len(progs) == 0 {
			continue
		}
		list := make([]map[string]any, 0, len(progs))
		for _, p := range progs {
			list = append(list, stalkerProgramme(c, p, now))
		}
		data[strconv.FormatInt(c.ID, 10)] = list
	}
	stalkerJSON(w, map[string]any{"data": data})
}

// stalkerDayEPG answers get_simple_data_table: the guide of one channel
// on one day, a page at a time, starting at the page with what is on now.
func (h *Handler) stalkerDayEPG(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	c, found, ok := h.stalkerChannelParam(w, r, sr)
	if !ok {
		return
	}
	if !found {
		stalkerJSON(w, stalkerPage(nil, 1, 0))
		return
	}
	now := time.Now()
	day, err := time.ParseInLocation("2006-01-02", r.FormValue("date"), time.Local)
	if err != nil {
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	var items []map[string]any
	current := -1
	for _, p := range h.channelProgrammes(r.Context(), sr.st, c, day, day.AddDate(0, 0, 1), false) {
		if !p.Start.After(now) && p.Stop.After(now) {
			current = len(items)
		}
		items = append(items, stalkerProgramme(c, p, now))
	}
	page := stalkerPageNumber(r)
	selected := 0
	if r.FormValue("p") == "0" || r.FormValue("p") == "" {
		if current >= 0 {
			page = current/stalkerPageSize + 1
			selected = current%stalkerPageSize + 1
		}
	}
	stalkerJSON(w, stalkerPage(items, page, selected))
}

// stalkerWeek lists the days players can pick in the guide.
func stalkerWeek(now time.Time) []map[string]any {
	out := []map[string]any{}
	for i := -1; i < 7; i++ {
		d := now.AddDate(0, 0, i)
		today := 0
		if i == 0 {
			today = 1
		}
		out = append(out, map[string]any{"f_human": d.Format("Mon 02.01"), "f_mysql": d.Format("2006-01-02"), "today": today})
	}
	return out
}

// ---- On Demand ----

// vodGroup is a Stalker VOD category: the movie and series categories of
// the same name together.
type vodGroup struct {
	id   string
	name string
	ids  map[int64]bool
}

func (h *Handler) vodGroups(g *vodGate) []vodGroup {
	var out []vodGroup
	byName := map[string]int{}
	for _, c := range append(h.vod.Categories(media.Movies), h.vod.Categories(media.Shows)...) {
		if !g.allowsCategory(c.ID) {
			continue
		}
		key := strings.ToLower(c.Name)
		i, ok := byName[key]
		if !ok {
			i = len(out)
			byName[key] = i
			out = append(out, vodGroup{id: strconv.FormatInt(c.ID, 10), name: c.Name, ids: map[int64]bool{}})
		}
		out[i].ids[c.ID] = true
	}
	return out
}

// stalkerOnDemand reports whether the user may watch On Demand, with the
// parental controls that apply. On an error it writes the response.
func (h *Handler) stalkerOnDemand(w http.ResponseWriter, r *http.Request, sr stalkerRequest) (*vodGate, bool) {
	ok, err := h.onDemand(r, sr.user)
	if err != nil {
		h.serverError(w, err)
		return nil, false
	}
	if !ok {
		return nil, false
	}
	g, err := h.vodGate(r.Context(), sr.user)
	if err != nil {
		h.serverError(w, err)
		return nil, false
	}
	return g, true
}

func (h *Handler) stalkerVODCategories(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	out := []map[string]any{}
	if gate, ok := h.stalkerOnDemand(w, r, sr); ok {
		out = append(out, map[string]any{"id": "*", "title": i18n.T(sr.st.Language, "All"), "alias": "*", "censored": 0})
		for _, g := range h.vodGroups(gate) {
			out = append(out, map[string]any{"id": g.id, "title": g.name, "alias": strings.ToLower(g.name), "censored": 0})
		}
	}
	stalkerJSON(w, out)
}

// seasonOffset keeps the ids of seasons apart from those of movies: a
// Stalker VOD list has one item per season, as old Stalker portals had.
const seasonOffset = 1_000_000_000_000

// vodEntry is one item of the Stalker VOD list: a movie or one season of
// a series.
type vodEntry struct {
	item     *media.Item // the movie or the series
	season   int
	episodes []*media.Item // of the season
	seasons  int           // number of seasons of the series
	added    time.Time
}

func (e vodEntry) id() int64 {
	if e.item.Type == media.Movie {
		return e.item.ID
	}
	return seasonOffset + e.item.ID*1000 + int64(e.season)
}

func (e vodEntry) title(lang string) string {
	if e.item.Type == media.Movie || e.seasons <= 1 && e.season > 0 {
		return e.item.Title
	}
	if e.season == 0 {
		return e.item.Title + " – " + i18n.T(lang, "Specials")
	}
	return e.item.Title + " – " + i18n.Tf(lang, "Season %d", e.season)
}

// episodeNumbers are the numbers players show for the episodes of a
// season: their own numbers, or 1, 2, 3, ... when those are missing or
// double.
func episodeNumbers(eps []*media.Item) []int {
	out := make([]int, len(eps))
	seen := map[int]bool{}
	own := true
	for i, ep := range eps {
		if ep.Number <= 0 || seen[ep.Number] {
			own = false
		}
		seen[ep.Number] = true
		out[i] = ep.Number
	}
	if !own {
		for i := range out {
			out[i] = i + 1
		}
	}
	return out
}

func (h *Handler) vodEntries() []vodEntry {
	var out []vodEntry
	for _, m := range h.vod.Movies() {
		out = append(out, vodEntry{item: m, added: m.Added})
	}
	for _, s := range h.vod.Shows() {
		var seasons []vodEntry
		for _, ep := range h.vod.Episodes(s.ID) {
			if n := len(seasons); n == 0 || seasons[n-1].season != ep.Season {
				seasons = append(seasons, vodEntry{item: s, season: ep.Season, added: s.Added})
			}
			e := &seasons[len(seasons)-1]
			e.episodes = append(e.episodes, ep)
			if ep.Added.After(e.added) {
				e.added = ep.Added
			}
		}
		for i := range seasons {
			seasons[i].seasons = len(seasons)
		}
		out = append(out, seasons...)
	}
	return out
}

func (h *Handler) stalkerVODList(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	gate, ok := h.stalkerOnDemand(w, r, sr)
	if !ok {
		stalkerJSON(w, stalkerPage(nil, 1, 0))
		return
	}
	lang := sr.st.Language
	var group *vodGroup
	if want := r.FormValue("category"); want != "" && want != "*" {
		for _, g := range h.vodGroups(gate) {
			if g.id == want {
				group = &g
				break
			}
		}
		if group == nil {
			stalkerJSON(w, stalkerPage(nil, 1, 0))
			return
		}
	}
	search := strings.ToLower(strings.TrimSpace(r.FormValue("search")))
	var entries []vodEntry
	for _, e := range h.vodEntries() {
		if !gate.allows(e.item) {
			continue
		}
		if group != nil && !slices.ContainsFunc(e.item.CategoryIDs, func(id int64) bool { return group.ids[id] }) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(e.item.Title), search) {
			continue
		}
		entries = append(entries, e)
	}
	switch r.FormValue("sortby") {
	case "name":
		slices.SortStableFunc(entries, func(a, b vodEntry) int {
			return cmp.Or(cmp.Compare(strings.ToLower(a.item.Title), strings.ToLower(b.item.Title)), cmp.Compare(a.season, b.season))
		})
	case "top", "rating":
		slices.SortStableFunc(entries, func(a, b vodEntry) int { return cmp.Compare(b.item.Rating, a.item.Rating) })
	default: // "added": newest first
		slices.SortStableFunc(entries, func(a, b vodEntry) int { return b.added.Compare(a.added) })
	}
	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		it := e.item
		rt, _ := rating(it)
		cat := ""
		if len(it.CategoryIDs) > 0 {
			cat = strconv.FormatInt(it.CategoryIDs[0], 10)
		}
		item := map[string]any{
			"id":               strconv.FormatInt(e.id(), 10),
			"name":             e.title(lang),
			"o_name":           it.Title,
			"description":      it.Plot,
			"director":         strings.Join(it.Directors, ", "),
			"actors":           strings.Join(it.Cast, ", "),
			"year":             year(it),
			"time":             int(it.Duration.Minutes()),
			"genres_str":       strings.Join(it.Genres, ", "),
			"category_id":      cat,
			"rating_imdb":      rt,
			"rating_kinopoisk": "",
			"rating_mpaa":      it.ContentRating,
			"age":              it.ContentRating,
			"screenshot_uri":   imageURL(sr.base, it, "poster"),
			"pic":              imageURL(sr.base, it, "poster"),
			"added":            e.added.Local().Format("2006-01-02 15:04:05"),
			"hd":               1,
			"censored":         0,
			"fav":              0,
			"lock":             0,
			"for_rent":         0,
			"has_files":        1,
			"series":           []int{},
		}
		if it.Type == media.Movie {
			item["cmd"] = "ffmpeg http://localhost/vod/" + strconv.FormatInt(it.ID, 10)
		} else {
			item["cmd"] = fmt.Sprintf("ffmpeg http://localhost/series/%d/%d", it.ID, e.season)
			item["series"] = episodeNumbers(e.episodes)
			item["is_series"] = "1"
		}
		items = append(items, item)
	}
	stalkerJSON(w, stalkerPage(items, stalkerPageNumber(r), 0))
}

// stalkerVODLink answers create_link for a movie, or for an episode of a
// season (the series parameter is the episode number).
func (h *Handler) stalkerVODLink(w http.ResponseWriter, r *http.Request, sr stalkerRequest) {
	nothing := func() { stalkerJSON(w, map[string]any{"id": 0, "cmd": "", "error": "nothing_to_play"}) }
	gate, ok := h.stalkerOnDemand(w, r, sr)
	if !ok {
		nothing()
		return
	}
	cmd := r.FormValue("cmd")
	var it *media.Item
	if m := seasonCmd.FindStringSubmatch(cmd); m != nil {
		show, _ := strconv.ParseInt(m[1], 10, 64)
		season, _ := strconv.Atoi(m[2])
		ep, _ := strconv.Atoi(r.FormValue("series"))
		var eps []*media.Item
		for _, e := range h.vod.Episodes(show) {
			if e.Season == season {
				eps = append(eps, e)
			}
		}
		for i, n := range episodeNumbers(eps) {
			if n == ep || ep == 0 && i == 0 {
				it = eps[i]
				break
			}
		}
	} else if m := movieCmd.FindStringSubmatch(cmd); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		it, _ = h.vod.Item(id, media.Movie)
	}
	if it == nil || !h.vodAllowed(gate, it) {
		nothing()
		return
	}
	kind := "vod"
	if it.Type == media.Episode {
		kind = "ep"
	}
	ext := ""
	if it.Container != "" {
		ext = "." + it.Container
	}
	stalkerJSON(w, map[string]any{"id": strconv.FormatInt(it.ID, 10), "cmd": "ffmpeg " + h.stalkerLink(r.Context(), sr, kind, it.ID, ext),
		"storage_id": "", "load": 0, "error": ""})
}

// ---- play links ----

// playLink is what a play link stands for.
type playLink struct {
	user, device int64
	kind         string // "ch", "vod" or "ep"
	id           int64
	expires      time.Time
}

// linkKey returns the key that signs play links, made once and stored so
// links keep working after a restart.
func (h *Handler) linkKey(ctx context.Context) ([]byte, error) {
	h.linkMu.Lock()
	defer h.linkMu.Unlock()
	if h.linkSecret != nil {
		return h.linkSecret, nil
	}
	v, err := h.store.Value(ctx, stalkerSecretKey)
	if errors.Is(err, store.ErrNotFound) || err == nil && len(v) < 32 {
		v = randomHex(32)
		err = h.store.SetValue(ctx, stalkerSecretKey, v)
	}
	if err != nil {
		return nil, err
	}
	h.linkSecret = []byte(v)
	return h.linkSecret, nil
}

func signLink(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// stalkerLink makes the play link of a channel ("ch"), movie ("vod") or
// episode ("ep") for the device of the request. ext (".m3u8", ".mkv", ...)
// only helps players pick a demuxer.
func (h *Handler) stalkerLink(ctx context.Context, sr stalkerRequest, kind string, id int64, ext string) string {
	key, err := h.linkKey(ctx)
	if err != nil {
		h.log.Error("making play link key", "err", err)
		return ""
	}
	if len(ext) > 6 || strings.ContainsAny(ext, "/?#&") {
		ext = ""
	}
	payload := fmt.Sprintf("%d.%d.%s.%d.%d", sr.user.ID, sr.device.ID, kind, id, time.Now().Add(stalkerLinkTTL).Unix())
	enc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return sr.base + "/stalker/play/" + enc + "." + signLink(key, payload) + "/stream" + ext
}

// verifyLink checks the signature and age of a play link token.
func (h *Handler) verifyLink(ctx context.Context, token string) (playLink, bool) {
	var l playLink
	enc, sig, ok := strings.Cut(token, ".")
	if !ok {
		return l, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return l, false
	}
	key, err := h.linkKey(ctx)
	if err != nil {
		return l, false
	}
	if !hmac.Equal([]byte(sig), []byte(signLink(key, string(raw)))) {
		return l, false
	}
	f := strings.Split(string(raw), ".")
	if len(f) != 5 {
		return l, false
	}
	l.user, _ = strconv.ParseInt(f[0], 10, 64)
	l.device, _ = strconv.ParseInt(f[1], 10, 64)
	l.kind = f[2]
	l.id, _ = strconv.ParseInt(f[3], 10, 64)
	exp, _ := strconv.ParseInt(f[4], 10, 64)
	l.expires = time.Unix(exp, 0)
	return l, time.Now().Before(l.expires)
}

// stalkerPlay plays a play link: it checks again that the device is
// still registered and the account active, and that the user may still
// watch the channel or movie.
func (h *Handler) stalkerPlay(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.stalkerOn(w, r); !ok {
		return
	}
	l, ok := h.verifyLink(r.Context(), r.PathValue("token"))
	if !ok {
		http.Error(w, "this link has expired, open the channel again", http.StatusForbidden)
		return
	}
	dev, err := h.store.UserDevice(r.Context(), l.device)
	if errors.Is(err, store.ErrNotFound) || err == nil && dev.UserID != l.user {
		http.Error(w, "unknown device", http.StatusForbidden)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	u, err := h.store.User(r.Context(), l.user)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !u.Active() {
		http.Error(w, "account is disabled or expired", http.StatusForbidden)
		return
	}
	h.seen(r, u, web.ClientIP(r))
	switch l.kind {
	case "ch":
		c, found, err := h.userChannel(r, u, l.id)
		if err != nil {
			h.serverError(w, err)
			return
		}
		if !found {
			http.NotFound(w, r)
			return
		}
		if !h.open(w, r, u, c.ID, c.Name, c.Kind) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, c.StreamURL, http.StatusFound)
	case "vod", "ep":
		allowed, err := h.onDemand(r, u)
		if err != nil {
			h.serverError(w, err)
			return
		}
		t := media.Movie
		if l.kind == "ep" {
			t = media.Episode
		}
		var it *media.Item
		if allowed {
			it, _ = h.vod.Item(l.id, t)
		}
		if it == nil {
			http.NotFound(w, r)
			return
		}
		h.playVOD(w, r, u, it)
	default:
		http.NotFound(w, r)
	}
}

// streamExt is the file extension of a stream URL, such as ".m3u8".
func streamExt(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return path.Ext(u.Path)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
