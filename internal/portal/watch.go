package portal

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// The web player lets users watch their TV channels, radio stations and
// On Demand in the browser from the account pages. The same rules apply as
// in their player: only what their packages include, and no more places
// at once than their connection limit allows.
//
// Live streams normally play straight from the broadcaster, as in the
// players. Browsers are stricter than players, though: they refuse an
// http stream on an https page, and they only read a stream from another
// website when that website allows it (CORS). For those streams the
// portal passes the stream on (/account/stream/...), rewriting the
// addresses in HLS playlists so every part goes through it. On Demand
// always goes through the portal so the media server's token stays on the
// server.

// streamTTL is how long the address of a passed-on stream works. The
// playlists of live streams are fetched again every few seconds, which
// renews the addresses in them.
const streamTTL = 12 * time.Hour

// keepAlive is how often the page tells the portal it is still playing,
// so the connection keeps its slot and the view keeps counting.
const keepAlive = 5 * time.Minute

// maxPlaylist is the largest HLS playlist the portal rewrites.
const maxPlaylist = 4 << 20

func (h *Handler) watchRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.UserSession))) {
	on := func(pattern string, fn func(http.ResponseWriter, *http.Request, store.UserSession)) {
		auth(pattern, h.webPlayerOn(fn))
	}
	on("GET /account/watch", h.watchPage)
	on("GET /account/watch/library", h.watchLibrary)
	on("GET /account/watch/series/{id}", h.watchSeries)
	on("GET /account/watch/now/{id}", h.watchNow)
	on("POST /account/watch/play", h.watchPlay)
	on("GET /account/stream/{token}", h.watchStream)
}

// webPlayerOn answers 404 while the admin has the web player turned off.
func (h *Handler) webPlayerOn(fn func(http.ResponseWriter, *http.Request, store.UserSession)) func(http.ResponseWriter, *http.Request, store.UserSession) {
	return func(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
		st, err := h.store.Settings(r.Context())
		if err != nil {
			h.serverError(w, err)
			return
		}
		if !st.WebPlayer {
			http.NotFound(w, r)
			return
		}
		fn(w, r, sess)
	}
}

// ---- page ----

// watchChannel is a channel or station on the watch page.
type watchChannel struct {
	ID       int64
	Name     string
	Logo     string
	Category string
}

type watchData struct {
	TV, Radio []watchChannel
	OnDemand  bool
	Active    bool
	// Config is read by watch.js: the CSRF token, texts and categories.
	Config map[string]any
}

func (h *Handler) watchPage(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	// The same channels as in the player: hidden groups and groups locked
	// by parental controls are left out. Favourites come first.
	view, _, err := h.playerView(r.Context(), sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	vod, err := h.onDemand(r, sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	lang := h.language(r, st)
	base := web.BaseURL(st.PublicURL, r)
	d := watchData{OnDemand: vod, Active: sess.User.Active()}
	var tvCats, radioCats []string
	fav := map[int64]bool{}
	for _, c := range view.Favourites {
		fav[c.ID] = true
	}
	chs := append([]store.Channel{}, view.Favourites...)
	for _, c := range view.Channels {
		if !fav[c.ID] {
			chs = append(chs, c)
		}
	}
	seen := map[string]bool{}
	for _, c := range chs {
		cat := c.Category
		if c.CategoryID == 0 {
			cat = i18n.T(lang, "Other")
		}
		wc := watchChannel{ID: c.ID, Name: c.Name, Logo: logoURL(base, c), Category: cat}
		key := c.Kind + "\x00" + cat
		if c.IsRadio() {
			d.Radio = append(d.Radio, wc)
			if !seen[key] {
				radioCats = append(radioCats, cat)
			}
		} else {
			d.TV = append(d.TV, wc)
			if !seen[key] {
				tvCats = append(tvCats, cat)
			}
		}
		seen[key] = true
	}
	messages := map[string]string{
		"limit":       i18n.T(lang, "You are already watching on as many devices as your account allows. Stop watching on another device and try again."),
		"unsupported": i18n.T(lang, "This stream cannot be played in a web browser. Watch it in the app on your TV box."),
		"failed":      i18n.T(lang, "This stream does not play right now. Try again later or pick another one."),
		"error":       i18n.T(lang, "Something went wrong. Reload the page and try again."),
		"loading":     i18n.T(lang, "Loading…"),
		"now":         i18n.T(lang, "Now"),
		"next":        i18n.T(lang, "Next"),
		"empty":       i18n.T(lang, "Nothing found."),
		"season":      i18n.T(lang, "Season %d"),
		"specials":    i18n.T(lang, "Specials"),
		"episode":     i18n.T(lang, "Episode %d"),
		"minutes":     i18n.T(lang, "%d min"),
		"all":         i18n.T(lang, "All categories"),
		"back":        i18n.T(lang, "Back"),
		"play":        i18n.T(lang, "Play"),
		"nolibrary":   i18n.T(lang, "There is nothing here yet."),
		"inactive":    i18n.T(lang, "Your account is not active, so nothing can be played."),
		"more":        i18n.T(lang, "Show more"),
	}
	d.Config = map[string]any{"csrf": sess.CSRF, "keepalive": int(keepAlive.Seconds()), "msg": messages,
		"cats": map[string][]string{"tv": tvCats, "radio": radioCats}}
	h.renderAccount(w, r, "watch", http.StatusOK, accountPage{Title: "Watch", Nav: "watch", Session: &sess, Data: d})
}

// ---- On Demand lists ----

type watchItem struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	Year     int     `json:"year,omitempty"`
	Poster   string  `json:"poster,omitempty"`
	Minutes  int     `json:"minutes,omitempty"`
	Rating   float64 `json:"rating,omitempty"`
	Category []int64 `json:"cats"`
}

type watchCategory struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// watchLibrary lists the movies (?type=movies) or series (?type=series)
// for the watch page.
func (h *Handler) watchLibrary(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ok, err := h.onDemand(r, sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	g, err := h.vodGate(r.Context(), sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	lt, list := media.Movies, h.vod.Movies()
	if r.FormValue("type") == "series" {
		lt, list = media.Shows, h.vod.Shows()
	}
	cats := []watchCategory{}
	for _, c := range h.vod.Categories(lt) {
		if g.allowsCategory(c.ID) {
			cats = append(cats, watchCategory{ID: c.ID, Name: c.Name})
		}
	}
	items := make([]watchItem, 0, len(list))
	for _, it := range list {
		if g.allows(it) {
			items = append(items, webItem(it))
		}
	}
	writeJSON(w, map[string]any{"categories": cats, "items": items})
}

func webItem(it *media.Item) watchItem {
	return watchItem{ID: it.ID, Title: it.Title, Year: it.Year, Poster: imageURL("", it, "poster"),
		Minutes: int(it.Duration.Minutes()), Rating: it.Rating, Category: categoryIDs(it)}
}

type watchEpisode struct {
	watchItem
	Number int    `json:"number"`
	Plot   string `json:"plot,omitempty"`
}

type watchSeason struct {
	Number   int            `json:"number"`
	Episodes []watchEpisode `json:"episodes"`
}

// watchSeries lists the seasons and episodes of a series.
func (h *Handler) watchSeries(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ok, err := h.onDemand(r, sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	id, valid := pathInt(r)
	if !ok || !valid {
		http.NotFound(w, r)
		return
	}
	g, err := h.vodGate(r.Context(), sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	show, found := h.vod.Item(id, media.Show)
	if !found || !g.allows(show) {
		http.NotFound(w, r)
		return
	}
	seasons := []watchSeason{}
	for _, ep := range h.vod.Episodes(show.ID) {
		if len(seasons) == 0 || seasons[len(seasons)-1].Number != ep.Season {
			seasons = append(seasons, watchSeason{Number: ep.Season})
		}
		s := &seasons[len(seasons)-1]
		s.Episodes = append(s.Episodes, watchEpisode{watchItem: webItem(ep), Number: ep.Number, Plot: ep.Plot})
	}
	writeJSON(w, map[string]any{"show": webItem(show), "plot": show.Plot, "seasons": seasons})
}

// ---- playing ----

// playInfo tells watch.js what to play and how.
type playInfo struct {
	Title string `json:"title"`
	// Type is "hls", "audio", "video" or "unsupported".
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
	// Fallback is the address through the portal, tried when URL fails.
	Fallback string `json:"fallback,omitempty"`
	Logo     string `json:"logo,omitempty"`
	Now      string `json:"now,omitempty"`
	Next     string `json:"next,omitempty"`
}

func jsonError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": code})
}

// watchPlay starts playback of kind=live (a channel or station) or
// kind=movie or kind=episode with the given id. It counts as opening a
// stream in a player, so the connection limit applies. The page calls it
// again with keepalive=1 while it plays.
func (h *Handler) watchPlay(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	u := sess.User
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "error")
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !u.Active() {
		jsonError(w, http.StatusForbidden, "inactive")
		return
	}
	h.seen(r, u, web.ClientIP(r))
	switch kind := r.FormValue("kind"); kind {
	case "live":
		c, found, err := h.userChannel(r, u, id)
		if err != nil {
			h.serverError(w, err)
			return
		}
		if !found {
			jsonError(w, http.StatusNotFound, "error")
			return
		}
		if !h.tryOpen(r, u, c.ID, c.Name, c.Kind) {
			jsonError(w, http.StatusForbidden, "limit")
			return
		}
		writeJSON(w, h.livePlay(r, st, u, c))
	case "movie", "episode":
		ok, err := h.onDemand(r, u)
		if err != nil {
			h.serverError(w, err)
			return
		}
		t := media.Movie
		if kind == "episode" {
			t = media.Episode
		}
		var it *media.Item
		if ok {
			it, ok = h.vod.Item(id, t)
		}
		if ok {
			g, err := h.vodGate(r.Context(), u)
			if err != nil {
				h.serverError(w, err)
				return
			}
			ok = h.vodAllowed(g, it)
		}
		if !ok {
			jsonError(w, http.StatusNotFound, "error")
			return
		}
		target, auth := h.vod.WebURL(it)
		if target == "" {
			jsonError(w, http.StatusNotFound, "error")
			return
		}
		title := h.vodTitle(it)
		if !h.tryOpen(r, u, 0, title, store.KindVOD) {
			jsonError(w, http.StatusForbidden, "limit")
			return
		}
		p := playInfo{Title: title, Type: "video", URL: h.streams.address(u.ID, target, auth),
			Logo: imageURL("", it, "backdrop")}
		if isPlaylistPath(target) {
			p.Type = "hls"
		}
		writeJSON(w, p)
	default:
		jsonError(w, http.StatusBadRequest, "error")
	}
}

// livePlay works out how the browser plays a channel or station.
func (h *Handler) livePlay(r *http.Request, st store.Settings, u store.User, c store.Channel) playInfo {
	base := web.BaseURL(st.PublicURL, r)
	p := playInfo{Title: c.Name, Logo: logoURL(base, c), Type: streamFormat(c)}
	if p.Type == "unsupported" {
		return p
	}
	through := h.streams.address(u.ID, c.StreamURL, nil)
	mixed := web.IsHTTPS(r) && strings.HasPrefix(strings.ToLower(c.StreamURL), "http:")
	switch {
	case st.WebPlayerProxy == store.WebPlayerAlways:
		p.URL = through
	case st.WebPlayerProxy == store.WebPlayerNever:
		p.URL = c.StreamURL
	case mixed:
		p.URL = through
	default:
		p.URL, p.Fallback = c.StreamURL, through
	}
	p.Now, p.Next = h.nowNext(r, st, c)
	return p
}

// nowNext is what is on a channel now and next, or for a radio station
// the song it plays.
func (h *Handler) nowNext(r *http.Request, st store.Settings, c store.Channel) (now, next string) {
	if h.radioGuide(c) {
		return h.currentSong(r.Context(), st, c, false).Title, ""
	}
	if c.EPGID == "" {
		return "", ""
	}
	t := time.Now()
	for _, p := range h.guide.Programmes(c.EPGID, t.Add(-12*time.Hour), t.Add(12*time.Hour)) {
		switch {
		case !p.Start.After(t) && p.Stop.After(t):
			now = p.Title
		case p.Start.After(t) && next == "":
			next = p.Title
		}
	}
	return now, next
}

// watchNow answers what a channel or station plays now, for the page to
// keep up to date while it plays.
func (h *Handler) watchNow(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	id, _ := pathInt(r)
	c, found, err := h.userChannel(r, sess.User, id)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	var now, next string
	if h.radioGuide(c) {
		now = h.currentSong(r.Context(), st, c, true).Title
	} else {
		now, next = h.nowNext(r, st, c)
	}
	writeJSON(w, map[string]string{"now": now, "next": next})
}

// streamFormat guesses from the address how a browser can play a stream.
// Browsers play HLS (with hls.js), the audio formats of radio streams and
// MP4 video, but not raw MPEG-TS, RTMP or DASH without more code.
func streamFormat(c store.Channel) string {
	u, err := url.Parse(c.StreamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "unsupported"
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".m3u8", ".m3u":
		return "hls"
	case ".mp4", ".m4v", ".webm":
		return "video"
	case ".ts", ".mpd", ".flv", ".mkv", ".avi":
		if c.IsRadio() {
			return "audio"
		}
		return "unsupported"
	}
	if c.IsRadio() {
		return "audio"
	}
	return "hls"
}

func isPlaylistPath(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	ext := strings.ToLower(path.Ext(u.Path))
	return ext == ".m3u8" || ext == ".m3u"
}

// ---- passing streams on ----

// streamTokens hides the source of a passed-on stream in its address.
// The address is encrypted with a key that only lives in memory, so it
// cannot be read (media server tokens stay secret) or changed (the portal
// only fetches addresses it handed out itself). After a restart the page
// asks for a new address.
type streamTokens struct {
	aead cipher.AEAD
	now  func() time.Time
}

func newStreamTokens() *streamTokens {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	return &streamTokens{aead: aead, now: time.Now}
}

// streamToken is what an address through the portal stands for.
type streamToken struct {
	User   int64      `json:"u"`
	Target string     `json:"t"`
	Auth   url.Values `json:"a,omitempty"` // added to addresses on the same server
	Host   string     `json:"h,omitempty"` // the server Auth belongs to
	Expiry int64      `json:"e"`
}

func (s *streamTokens) seal(t streamToken) string {
	t.Expiry = s.now().Add(streamTTL).Unix()
	b, _ := json.Marshal(t)
	nonce := make([]byte, s.aead.NonceSize())
	rand.Read(nonce)
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, b, nil))
}

var errBadToken = errors.New("invalid or expired stream address")

func (s *streamTokens) open(token string) (streamToken, error) {
	var t streamToken
	b, err := base64.RawURLEncoding.DecodeString(token)
	n := s.aead.NonceSize()
	if err != nil || len(b) < n {
		return t, errBadToken
	}
	plain, err := s.aead.Open(nil, b[:n], b[n:], nil)
	if err != nil || json.Unmarshal(plain, &t) != nil || s.now().Unix() > t.Expiry {
		return t, errBadToken
	}
	return t, nil
}

// address is the portal address that passes target on to user.
func (s *streamTokens) address(user int64, target string, auth url.Values) string {
	t := streamToken{User: user, Target: target}
	if len(auth) > 0 {
		if u, err := url.Parse(target); err == nil {
			t.Auth, t.Host = auth, u.Host
		}
	}
	return "/account/stream/" + s.seal(t)
}

// streamClient fetches passed-on streams. It has no overall timeout:
// radio streams never end. Connecting and the first answer do get one.
var streamClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = 20 * time.Second
	return &http.Client{Transport: tr}
}()

// watchStream passes a stream on to the browser: HLS playlists with their
// addresses rewritten to go through the portal too, anything else as it
// comes.
func (h *Handler) watchStream(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	t, err := h.streams.open(r.PathValue("token"))
	if err != nil || t.User != sess.User.ID || !sess.User.Active() {
		http.Error(w, "invalid or expired stream address", http.StatusNotFound)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, t.Target, nil)
	if err != nil {
		http.Error(w, "invalid stream address", http.StatusBadGateway)
		return
	}
	for _, k := range []string{"Range", "If-Range", "Accept", "User-Agent"} {
		if v := r.Header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL may hold a token
		}
		if r.Context().Err() == nil {
			h.log.Warn("passing on stream", "host", req.URL.Host, "err", err)
		}
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "no-store")
	if resp.StatusCode >= 400 {
		w.WriteHeader(resp.StatusCode)
		return
	}
	body := bufio.NewReaderSize(resp.Body, 64<<10)
	if playlistResponse(resp, body) {
		b, err := io.ReadAll(io.LimitReader(body, maxPlaylist+1))
		if err != nil || len(b) > maxPlaylist {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		out := rewritePlaylist(b, resp.Request.URL, func(abs *url.URL) string {
			nt := streamToken{User: t.User, Auth: t.Auth, Host: t.Host}
			if len(t.Auth) > 0 && abs.Host == t.Host {
				q := abs.Query()
				for k, v := range t.Auth {
					if !q.Has(k) {
						q[k] = v
					}
				}
				abs.RawQuery = q.Encode()
			}
			nt.Target = abs.String()
			return "/account/stream/" + h.streams.seal(nt)
		})
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write(out)
		return
	}
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	done := h.activity.Proxy.Start()
	defer done()
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			h.activity.Proxy.Add(n)
			rc.Flush() // radio streams trickle in; pass them on right away
		}
		if err != nil {
			return
		}
	}
}

// playlistResponse reports whether a response is an HLS playlist.
func playlistResponse(resp *http.Response, body *bufio.Reader) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "mpegurl") {
		return true
	}
	if strings.HasPrefix(ct, "video/") || strings.HasPrefix(ct, "audio/mpeg") || strings.HasPrefix(ct, "audio/aac") {
		return false
	}
	head, _ := body.Peek(7)
	return string(head) == "#EXTM3U"
}

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// rewritePlaylist replaces every address in an HLS playlist (the lines
// that are not tags, and the URI attributes of tags) with mint's address
// for it. Addresses are resolved against base first; ones that are not
// http(s), such as data: URIs or DRM key ids, stay as they are.
func rewritePlaylist(b []byte, base *url.URL, mint func(*url.URL) string) []byte {
	resolve := func(ref string) (string, bool) {
		u, err := base.Parse(strings.TrimSpace(ref))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return ref, false
		}
		return mint(u), true
	}
	var out bytes.Buffer
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			out.WriteString(line)
		case strings.HasPrefix(trimmed, "#"):
			out.WriteString(uriAttr.ReplaceAllStringFunc(line, func(m string) string {
				ref := uriAttr.FindStringSubmatch(m)[1]
				if nu, ok := resolve(ref); ok {
					return `URI="` + nu + `"`
				}
				return m
			}))
		default:
			nu, _ := resolve(trimmed)
			out.WriteString(nu)
		}
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}
