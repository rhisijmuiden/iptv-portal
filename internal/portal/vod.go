package portal

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// This file serves On Demand: the movies and series of the Jellyfin and
// Plex servers, through the VOD and series parts of the Xtream Codes API.
// Unlike live channels, playback normally goes through the portal, so the
// media server's token never reaches the players.

// onDemand reports whether the user may watch On Demand.
func (h *Handler) onDemand(r *http.Request, u store.User) (bool, error) {
	if h.vod == nil {
		return false, nil
	}
	return h.store.UserOnDemand(r.Context(), u.ID)
}

func vodCategories(cats []media.Category) []map[string]any {
	out := []map[string]any{}
	for _, c := range cats {
		out = append(out, map[string]any{
			"category_id": strconv.FormatInt(c.ID, 10), "category_name": c.Name, "parent_id": 0})
	}
	return out
}

func rating(it *media.Item) (string, float64) {
	if it.Rating == 0 {
		return "", 0
	}
	return strconv.FormatFloat(it.Rating, 'f', 1, 64), float64(int(it.Rating*5+0.5)) / 10
}

func duration(it *media.Item) string {
	s := int(it.Duration.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}

// imageURL is the portal address of an item's poster or backdrop.
func imageURL(base string, it *media.Item, kind string) string {
	if (kind == "poster" && it.Poster == "") || (kind == "backdrop" && it.Backdrop == "") {
		return ""
	}
	return base + "/vod-image/" + strconv.FormatInt(it.ID, 10) + "/" + kind
}

func backdrops(base string, it *media.Item) []string {
	if u := imageURL(base, it, "backdrop"); u != "" {
		return []string{u}
	}
	return []string{}
}

func year(it *media.Item) string {
	if it.Year == 0 {
		return ""
	}
	return strconv.Itoa(it.Year)
}

func released(it *media.Item) string {
	if it.Released != "" {
		return it.Released
	}
	return year(it)
}

func (h *Handler) vodBase(r *http.Request) (string, error) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		return "", err
	}
	return web.BaseURL(st.PublicURL, r), nil
}

// vodAction answers the On Demand actions of player_api.php.
func (h *Handler) vodAction(w http.ResponseWriter, r *http.Request, u store.User, action string) {
	ok, err := h.onDemand(r, u)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !ok {
		if strings.HasSuffix(action, "_info") {
			writeJSON(w, map[string]any{})
		} else {
			writeJSON(w, []any{})
		}
		return
	}
	base, err := h.vodBase(r)
	if err != nil {
		h.serverError(w, err)
		return
	}
	switch action {
	case "get_vod_categories":
		writeJSON(w, vodCategories(h.vod.Categories(media.Movies)))
	case "get_series_categories":
		writeJSON(w, vodCategories(h.vod.Categories(media.Shows)))
	case "get_vod_streams":
		h.vodStreams(w, r, base)
	case "get_series":
		h.seriesList(w, r, base)
	case "get_vod_info":
		h.vodInfo(w, r, base)
	case "get_series_info":
		h.seriesInfo(w, r, base)
	}
}

func (h *Handler) vodStreams(w http.ResponseWriter, r *http.Request, base string) {
	want := r.FormValue("category_id")
	out := []map[string]any{}
	for i, it := range h.vod.Movies() {
		cat := strconv.FormatInt(it.CategoryID, 10)
		if want != "" && want != cat {
			continue
		}
		rt, rt5 := rating(it)
		out = append(out, map[string]any{
			"num":                 i + 1,
			"name":                it.Title,
			"title":               it.Title,
			"year":                year(it),
			"stream_type":         "movie",
			"stream_id":           it.ID,
			"stream_icon":         imageURL(base, it, "poster"),
			"rating":              rt,
			"rating_5based":       rt5,
			"added":               unix(it.Added),
			"category_id":         cat,
			"category_ids":        []int64{it.CategoryID},
			"container_extension": it.Container,
			"custom_sid":          "",
			"direct_source":       "",
		})
	}
	writeJSON(w, out)
}

func (h *Handler) vodInfo(w http.ResponseWriter, r *http.Request, base string) {
	id, _ := strconv.ParseInt(r.FormValue("vod_id"), 10, 64)
	it, ok := h.vod.Item(id, media.Movie)
	if !ok {
		writeJSON(w, map[string]any{})
		return
	}
	rt, _ := rating(it)
	poster := imageURL(base, it, "poster")
	writeJSON(w, map[string]any{
		"info": map[string]any{
			"name":            it.Title,
			"o_name":          it.Title,
			"movie_image":     poster,
			"cover_big":       poster,
			"backdrop_path":   backdrops(base, it),
			"tmdb_id":         it.TMDBID,
			"plot":            it.Plot,
			"description":     it.Plot,
			"genre":           strings.Join(it.Genres, ", "),
			"director":        strings.Join(it.Directors, ", "),
			"cast":            strings.Join(it.Cast, ", "),
			"actors":          strings.Join(it.Cast, ", "),
			"releasedate":     released(it),
			"year":            year(it),
			"rating":          rt,
			"age":             it.ContentRating,
			"duration_secs":   int(it.Duration.Seconds()),
			"duration":        duration(it),
			"youtube_trailer": "",
		},
		"movie_data": map[string]any{
			"stream_id":           it.ID,
			"name":                it.Title,
			"added":               unix(it.Added),
			"category_id":         strconv.FormatInt(it.CategoryID, 10),
			"container_extension": it.Container,
			"custom_sid":          "",
			"direct_source":       "",
		},
	})
}

func (h *Handler) showInfo(base string, it *media.Item, num int) map[string]any {
	rt, rt5 := rating(it)
	lastModified := it.Added
	for _, ep := range h.vod.Episodes(it.ID) {
		if ep.Added.After(lastModified) {
			lastModified = ep.Added
		}
	}
	return map[string]any{
		"num":              num,
		"name":             it.Title,
		"title":            it.Title,
		"year":             year(it),
		"series_id":        it.ID,
		"cover":            imageURL(base, it, "poster"),
		"plot":             it.Plot,
		"cast":             strings.Join(it.Cast, ", "),
		"director":         strings.Join(it.Directors, ", "),
		"genre":            strings.Join(it.Genres, ", "),
		"releaseDate":      released(it),
		"last_modified":    unix(lastModified),
		"rating":           rt,
		"rating_5based":    rt5,
		"backdrop_path":    backdrops(base, it),
		"youtube_trailer":  "",
		"episode_run_time": "",
		"category_id":      strconv.FormatInt(it.CategoryID, 10),
		"category_ids":     []int64{it.CategoryID},
	}
}

func (h *Handler) seriesList(w http.ResponseWriter, r *http.Request, base string) {
	want := r.FormValue("category_id")
	out := []map[string]any{}
	for i, it := range h.vod.Shows() {
		if want != "" && want != strconv.FormatInt(it.CategoryID, 10) {
			continue
		}
		out = append(out, h.showInfo(base, it, i+1))
	}
	writeJSON(w, out)
}

func (h *Handler) seriesInfo(w http.ResponseWriter, r *http.Request, base string) {
	id, _ := strconv.ParseInt(r.FormValue("series_id"), 10, 64)
	show, ok := h.vod.Item(id, media.Show)
	if !ok {
		writeJSON(w, map[string]any{})
		return
	}
	episodes := map[string][]map[string]any{}
	var seasons []map[string]any
	for _, ep := range h.vod.Episodes(show.ID) {
		key := strconv.Itoa(ep.Season)
		if _, ok := episodes[key]; !ok {
			name := "Season " + key
			if ep.Season == 0 {
				name = "Specials"
			}
			seasons = append(seasons, map[string]any{
				"id": ep.Season, "season_number": ep.Season, "name": name,
				"cover": imageURL(base, show, "poster"), "cover_big": imageURL(base, show, "poster"),
				"air_date": "", "overview": "", "episode_count": 0,
			})
		}
		seasons[len(seasons)-1]["episode_count"] = seasons[len(seasons)-1]["episode_count"].(int) + 1
		rt, _ := rating(ep)
		img := imageURL(base, ep, "poster")
		if img == "" {
			img = imageURL(base, show, "poster")
		}
		episodes[key] = append(episodes[key], map[string]any{
			"id":                  strconv.FormatInt(ep.ID, 10),
			"episode_num":         ep.Number,
			"title":               ep.Title,
			"container_extension": ep.Container,
			"season":              ep.Season,
			"added":               unix(ep.Added),
			"custom_sid":          "",
			"direct_source":       "",
			"info": map[string]any{
				"movie_image":   img,
				"plot":          ep.Plot,
				"releasedate":   ep.Released,
				"rating":        rt,
				"duration_secs": int(ep.Duration.Seconds()),
				"duration":      duration(ep),
			},
		})
	}
	if seasons == nil {
		seasons = []map[string]any{}
	}
	info := h.showInfo(base, show, 1)
	delete(info, "num")
	writeJSON(w, map[string]any{"seasons": seasons, "info": info, "episodes": episodes})
}

// ---- playback ----

// vodPlay handles /movie/{user}/{pass}/{id}.{ext} and
// /series/{user}/{pass}/{id}.{ext}.
func (h *Handler) vodPlay(t media.ItemType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := h.auth(w, r, r.PathValue("user"), r.PathValue("pass"))
		if !ok {
			return
		}
		allowed, err := h.onDemand(r, u)
		if err != nil {
			h.serverError(w, err)
			return
		}
		file := r.PathValue("file")
		id, err := strconv.ParseInt(strings.TrimSuffix(file, path.Ext(file)), 10, 64)
		if !allowed || err != nil {
			http.NotFound(w, r)
			return
		}
		it, ok := h.vod.Item(id, t)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !h.open(w, r, u, 0, h.vodTitle(it)) {
			return
		}
		target, direct := h.vod.URL(it, it.Stream)
		if target == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if direct {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		h.proxy(w, r, target)
	}
}

// vodTitle names a movie or episode for the dashboard.
func (h *Handler) vodTitle(it *media.Item) string {
	if it.Type != media.Episode {
		return it.Title
	}
	show := "Series"
	if s, ok := h.vod.Item(it.ShowID, media.Show); ok {
		show = s.Title
	}
	return fmt.Sprintf("%s S%02dE%02d", show, it.Season, it.Number)
}

// vodImage serves posters and backdrops. Players load images without
// credentials, so this needs no login; it only serves images of offered
// items.
func (h *Handler) vodImage(w http.ResponseWriter, r *http.Request) {
	if h.vod == nil {
		http.NotFound(w, r)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var it *media.Item
	for _, t := range []media.ItemType{media.Movie, media.Show, media.Episode} {
		if found, ok := h.vod.Item(id, t); ok {
			it = found
			break
		}
	}
	if it == nil {
		http.NotFound(w, r)
		return
	}
	var p string
	switch r.PathValue("kind") {
	case "poster":
		p = it.Poster
	case "backdrop":
		p = it.Backdrop
	}
	target, _ := h.vod.URL(it, p)
	if target == "" {
		http.NotFound(w, r)
		return
	}
	h.proxy(w, r, target)
}

// proxy streams target to the client, passing range requests on so players
// can seek.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, target string) {
	dst, err := url.Parse(target)
	if err != nil {
		h.serverError(w, err)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = dst
			pr.Out.Host = dst.Host
			// Pass on only what the media server needs; never the player's
			// cookies or credentials.
			hdr := http.Header{}
			for _, k := range []string{"Range", "If-Range", "Accept", "User-Agent", "If-None-Match", "If-Modified-Since"} {
				if v := pr.In.Header.Values(k); len(v) > 0 {
					hdr[k] = v
				}
			}
			pr.Out.Header = hdr
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err // the URL holds the token
			}
			if r.Context().Err() == nil { // not just a player that stopped
				h.log.Warn("streaming from media server", "host", dst.Host, "err", err)
			}
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}
