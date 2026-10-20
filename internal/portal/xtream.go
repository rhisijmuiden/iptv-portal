package portal

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// This file implements the parts of the Xtream Codes API that IPTV players
// (MyTVOnline, TiviMate, IPTV Smarters, ...) use for live TV (On Demand is
// in vod.go), plus the
// classic get.php playlist and xmltv.php guide. Streams are never proxied:
// /live/... answers with a redirect to the broadcaster's own URL.

// auth checks the username and password of a player request. On failure it
// writes the response and returns false.
func (h *Handler) auth(w http.ResponseWriter, r *http.Request, username, password string) (store.User, bool) {
	ip := web.ClientIP(r)
	if !h.authLimiter.Allow(ip) {
		http.Error(w, "too many failed logins, try again later", http.StatusTooManyRequests)
		return store.User{}, false
	}
	u, err := h.store.AuthenticateUser(r.Context(), username, password)
	switch {
	case errors.Is(err, store.ErrInvalidLogin):
		h.authLimiter.Fail(ip)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return u, false
	case errors.Is(err, store.ErrUserInactive):
		http.Error(w, "account is disabled or expired", http.StatusForbidden)
		return u, false
	case err != nil:
		h.serverError(w, err)
		return u, false
	}
	h.authLimiter.Reset(ip)
	h.activity.Seen(u.ID, u.Username, ip, r.UserAgent())
	return u, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// ---- player_api.php ----

func (h *Handler) playerAPI(w http.ResponseWriter, r *http.Request) {
	username, password := r.FormValue("username"), r.FormValue("password")
	ip := web.ClientIP(r)
	if !h.authLimiter.Allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	u, err := h.store.AuthenticateUser(r.Context(), username, password)
	if errors.Is(err, store.ErrInvalidLogin) {
		h.authLimiter.Fail(ip)
		writeJSON(w, map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	if err != nil && !errors.Is(err, store.ErrUserInactive) {
		h.serverError(w, err)
		return
	}
	h.authLimiter.Reset(ip)
	active := err == nil
	if active {
		h.activity.Seen(u.ID, u.Username, ip, r.UserAgent())
	}

	action := r.FormValue("action")
	if action == "" {
		st, err := h.store.Settings(r.Context())
		if err != nil {
			h.serverError(w, err)
			return
		}
		writeJSON(w, map[string]any{
			"user_info":   userInfo(u, password, h.activity.ActiveConnections(u.ID)),
			"server_info": serverInfo(web.BaseURL(st.PublicURL, r)),
		})
		return
	}
	if !active {
		writeJSON(w, []any{})
		return
	}
	switch action {
	case "get_live_categories":
		h.liveCategories(w, r, u)
	case "get_live_streams":
		h.liveStreams(w, r, u)
	case "get_short_epg":
		h.shortEPG(w, r, u, false)
	case "get_simple_data_table":
		h.shortEPG(w, r, u, true)
	case "get_vod_categories", "get_vod_streams", "get_series_categories", "get_series",
		"get_vod_info", "get_series_info":
		h.vodAction(w, r, u, action)
	default:
		writeJSON(w, []any{})
	}
}

func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// userInfo describes the account; a max_connections of "0" means no limit.
func userInfo(u store.User, password string, activeCons int) map[string]any {
	status := "Active"
	switch {
	case !u.Enabled:
		status = "Disabled"
	case u.Expired():
		status = "Expired"
	}
	var exp any // null means no expiry
	if u.ExpiresAt != nil {
		exp = unix(*u.ExpiresAt)
	}
	return map[string]any{
		"username":               u.Username,
		"password":               password,
		"message":                "",
		"auth":                   1,
		"status":                 status,
		"exp_date":               exp,
		"is_trial":               "0",
		"active_cons":            strconv.Itoa(activeCons),
		"created_at":             unix(u.CreatedAt),
		"max_connections":        strconv.Itoa(u.ConnectionLimit()),
		"allowed_output_formats": []string{"m3u8", "ts"},
	}
}

func serverInfo(base string) map[string]any {
	u, _ := url.Parse(base)
	scheme, host, port := "http", base, "80"
	if u != nil {
		scheme, host = u.Scheme, u.Hostname()
		port = u.Port()
		if port == "" {
			port = map[string]string{"http": "80", "https": "443"}[scheme]
		}
	}
	httpsPort := "443"
	if scheme == "https" {
		httpsPort = port
	}
	now := time.Now()
	return map[string]any{
		"url":             host,
		"port":            port,
		"https_port":      httpsPort,
		"server_protocol": scheme,
		"rtmp_port":       "0",
		"timezone":        timezoneName(),
		"timestamp_now":   now.Unix(),
		"time_now":        now.Format("2006-01-02 15:04:05"),
	}
}

// timezoneName returns the IANA name of the server's time zone (Go only
// reports "Local" when it comes from /etc/localtime).
func timezoneName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			return tz
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

// categoryID is the Xtream category id of a channel; uncategorised
// channels are grouped under "0".
func categoryID(c store.Channel) string { return strconv.FormatInt(c.CategoryID, 10) }

func (h *Handler) liveCategories(w http.ResponseWriter, r *http.Request, u store.User) {
	chs, err := h.store.UserChannels(r.Context(), u.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, c := range chs {
		id := categoryID(c)
		if seen[id] {
			continue
		}
		seen[id] = true
		name := c.Category
		if name == "" {
			name = "Other"
		}
		out = append(out, map[string]any{"category_id": id, "category_name": name, "parent_id": 0})
	}
	writeJSON(w, out)
}

func (h *Handler) liveStreams(w http.ResponseWriter, r *http.Request, u store.User) {
	chs, err := h.store.UserChannels(r.Context(), u.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	want := r.FormValue("category_id")
	out := []map[string]any{}
	for i, c := range chs {
		if want != "" && want != categoryID(c) {
			continue
		}
		out = append(out, map[string]any{
			"num":                 i + 1,
			"name":                c.Name,
			"stream_type":         "live",
			"stream_id":           c.ID,
			"stream_icon":         c.LogoURL,
			"epg_channel_id":      nullable(c.EPGID),
			"added":               "0",
			"category_id":         categoryID(c),
			"category_ids":        []int64{c.CategoryID},
			"custom_sid":          "",
			"tv_archive":          0,
			"direct_source":       "",
			"tv_archive_duration": 0,
		})
	}
	writeJSON(w, out)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (h *Handler) userChannel(r *http.Request, u store.User, id int64) (store.Channel, bool, error) {
	chs, err := h.store.UserChannels(r.Context(), u.ID)
	if err != nil {
		return store.Channel{}, false, err
	}
	for _, c := range chs {
		if c.ID == id {
			return c, true, nil
		}
	}
	return store.Channel{}, false, nil
}

func (h *Handler) shortEPG(w http.ResponseWriter, r *http.Request, u store.User, all bool) {
	id, _ := strconv.ParseInt(r.FormValue("stream_id"), 10, 64)
	c, ok, err := h.userChannel(r, u, id)
	if err != nil {
		h.serverError(w, err)
		return
	}
	listings := []map[string]any{}
	if ok && c.EPGID != "" {
		now := time.Now()
		from, until := now, now.Add(7*24*time.Hour)
		if all {
			from = now.Add(-6 * time.Hour)
		}
		limit, _ := strconv.Atoi(r.FormValue("limit"))
		if limit <= 0 && !all {
			limit = 4
		}
		for _, p := range h.guide.Programmes(c.EPGID, from, until) {
			if limit > 0 && len(listings) >= limit {
				break
			}
			l := map[string]any{
				"id":              strconv.FormatInt(p.Start.Unix(), 10),
				"epg_id":          c.EPGID,
				"title":           base64.StdEncoding.EncodeToString([]byte(p.Title)),
				"lang":            "",
				"start":           p.Start.Local().Format("2006-01-02 15:04:05"),
				"end":             p.Stop.Local().Format("2006-01-02 15:04:05"),
				"description":     base64.StdEncoding.EncodeToString([]byte(p.Desc)),
				"channel_id":      c.EPGID,
				"start_timestamp": unix(p.Start),
				"stop_timestamp":  unix(p.Stop),
				"has_archive":     0,
			}
			if all {
				playing := 0
				if !p.Start.After(now) && p.Stop.After(now) {
					playing = 1
				}
				l["now_playing"] = playing
			}
			listings = append(listings, l)
		}
	}
	writeJSON(w, map[string]any{"epg_listings": listings})
}

// ---- stream redirect ----

// liveRedirect handles /live/{user}/{pass}/{id}.{ext} by redirecting the
// player to the channel's own stream URL.
func (h *Handler) liveRedirect(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth(w, r, r.PathValue("user"), r.PathValue("pass"))
	if !ok {
		return
	}
	file := r.PathValue("file")
	id, err := strconv.ParseInt(strings.TrimSuffix(file, path.Ext(file)), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, found, err := h.userChannel(r, u, id)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if !h.open(w, r, u, c.ID, c.Name) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, c.StreamURL, http.StatusFound)
}

// open records that the player starts watching, or refuses it with 403
// when the user already watches from as many places as allowed.
func (h *Handler) open(w http.ResponseWriter, r *http.Request, u store.User, channelID int64, title string) bool {
	limit := u.ConnectionLimit()
	if h.activity.Open(u.ID, u.Username, web.ClientIP(r), r.UserAgent(), channelID, title, limit) {
		return true
	}
	h.log.Info("connection limit reached", "username", u.Username, "ip", web.ClientIP(r), "limit", limit)
	http.Error(w, "maximum number of connections reached", http.StatusForbidden)
	return false
}

// ---- get.php / xmltv.php ----

func m3uAttr(s string) string {
	return strings.NewReplacer(`"`, `'`, "\n", " ", "\r", " ").Replace(s)
}

func (h *Handler) getPlaylist(w http.ResponseWriter, r *http.Request) {
	username, password := r.FormValue("username"), r.FormValue("password")
	u, ok := h.auth(w, r, username, password)
	if !ok {
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	chs, err := h.store.UserChannels(r.Context(), u.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	base := web.BaseURL(st.PublicURL, r)
	creds := url.Values{"username": {username}, "password": {password}}.Encode()
	ext := "m3u8"
	if r.FormValue("output") == "ts" || r.FormValue("output") == "mpegts" {
		ext = "ts"
	}
	plus := r.FormValue("type") != "m3u"

	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="playlist.m3u"`)
	w.Header().Set("Cache-Control", "no-store")
	bw := bufio.NewWriter(w)
	epgURL := base + "/xmltv.php?" + creds
	bw.WriteString(`#EXTM3U url-tvg="` + epgURL + `" x-tvg-url="` + epgURL + "\"\n")
	for _, c := range chs {
		if plus {
			bw.WriteString(`#EXTINF:-1 tvg-id="` + m3uAttr(c.EPGID) + `" tvg-name="` + m3uAttr(c.Name) +
				`" tvg-logo="` + m3uAttr(c.LogoURL) + `" group-title="` + m3uAttr(c.Category) + `",` + m3uAttr(c.Name) + "\n")
		} else {
			bw.WriteString("#EXTINF:-1," + m3uAttr(c.Name) + "\n")
		}
		bw.WriteString(base + "/live/" + url.PathEscape(username) + "/" + url.PathEscape(password) + "/" +
			strconv.FormatInt(c.ID, 10) + "." + ext + "\n")
	}
	bw.Flush()
}

func (h *Handler) xmltv(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth(w, r, r.FormValue("username"), r.FormValue("password"))
	if !ok {
		return
	}
	chs, err := h.store.UserChannels(r.Context(), u.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	var out []epg.Channel
	for _, c := range chs {
		if c.EPGID != "" {
			out = append(out, epg.Channel{ID: c.EPGID, Name: c.Name, Logo: c.LogoURL})
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.guide.WriteXMLTV(w, out); err != nil {
		h.log.Warn("writing guide", "err", err)
	}
}
