package admin

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rhisijmuiden/iptv-portal/internal/radiobrowser"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// radioPageSize is the number of stations per page on the Radio Browser
// page.
const radioPageSize = 100

// Category choices on the Radio Browser page besides an existing category
// id. The iptv-org page uses "@country" too.
const (
	categoryFromTag      = "@tag"
	categoryFromLanguage = "@language"
)

type radioRow struct {
	radiobrowser.Station
	Added bool
}

type radioBrowseData struct {
	Filter    radiobrowser.Filter
	Genre     string // category name for the searched tag, e.g. "Radio Pop"
	Page      int
	Rows      []radioRow
	PrevURL   string
	NextURL   string
	Countries []radiobrowser.Item
	Languages []radiobrowser.Item
	Tags      []radiobrowser.Item
	Own       []store.Category
	Packages  []store.Package
	Back      string
}

func (h *Handler) radioBrowse(w http.ResponseWriter, r *http.Request, sess store.Session) {
	p := page{Title: "Browse Radio Browser", Nav: "radiobrowse", Session: &sess}
	q := r.URL.Query()
	f := radiobrowser.Filter{Name: q.Get("q"), CountryCode: defaultCountry, Language: q.Get("language"),
		Tag: q.Get("tag"), Order: q.Get("order"), Limit: radioPageSize}
	if q.Has("country") {
		f.CountryCode = q.Get("country")
	}
	pg, _ := strconv.Atoi(q.Get("page"))
	pg = max(pg, 1)
	f.Offset = (pg - 1) * radioPageSize

	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	data := radioBrowseData{Filter: f, Page: pg, Back: r.URL.RequestURI()}
	if tag := strings.TrimSpace(f.Tag); tag != "" {
		data.Genre = "Radio " + capitalize(tag)
	}
	var err error
	if data.Countries, err = h.radio.Countries(ctx); err == nil {
		if data.Languages, err = h.radio.Languages(ctx); err == nil {
			data.Tags, err = h.radio.Tags(ctx)
		}
	}
	var stations []radiobrowser.Station
	if err == nil {
		stations, err = h.radio.Search(ctx, f)
	}
	if err != nil {
		p.Error = tr(r, "Could not reach Radio Browser: %s", err.Error())
		h.render(w, r, "radiobrowse", p)
		return
	}
	added, err := h.store.SourceIDs(r.Context(), store.KindRadio)
	if err != nil {
		h.serverError(w, err)
		return
	}
	urls, err := h.store.StreamURLs(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	if data.Own, err = h.store.CategoriesOf(r.Context(), store.KindRadio); err != nil {
		h.serverError(w, err)
		return
	}
	if data.Packages, err = h.store.Packages(r.Context()); err != nil {
		h.serverError(w, err)
		return
	}
	for _, s := range stations {
		data.Rows = append(data.Rows, radioRow{Station: s, Added: added[s.UUID] || urls[s.URL]})
	}
	if pg > 1 {
		data.PrevURL = pageURL(r, pg-1)
	}
	if len(stations) == radioPageSize {
		data.NextURL = pageURL(r, pg+1)
	}
	p.Data = data
	h.render(w, r, "radiobrowse", p)
}

// pageURL is the current address with another page number.
func pageURL(r *http.Request, n int) string {
	q := r.URL.Query()
	q.Del("ok")
	q.Del("err")
	q.Set("page", strconv.Itoa(n))
	return r.URL.Path + "?" + q.Encode()
}

func (h *Handler) radioBrowseAdd(w http.ResponseWriter, r *http.Request, sess store.Session) {
	back := backTo(r, "/radio/browse")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var ids []string
	seen := map[string]bool{}
	for _, id := range r.Form["station"] {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		http.Redirect(w, r, withFlash(back, "Select at least one station to add."), http.StatusSeeOther)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	stations, err := h.radio.ByUUID(ctx, ids)
	if err != nil {
		redirectError(w, r, back, tr(r, "Could not reach Radio Browser: %s", err.Error()))
		return
	}

	// Work out the category every station goes into.
	choice := r.FormValue("target")
	fixed := strings.TrimSpace(r.FormValue("new_category"))
	if fixed == "" && !strings.HasPrefix(choice, "@") && choice != "" {
		id, _ := strconv.ParseInt(choice, 10, 64)
		own, err := h.store.CategoriesOf(r.Context(), store.KindRadio)
		if err != nil {
			h.serverError(w, err)
			return
		}
		for _, c := range own {
			if c.ID == id {
				fixed = c.Name
			}
		}
	}
	filterTag := strings.TrimSpace(r.FormValue("filter_tag"))

	var entries []store.ImportEntry
	groups := map[string]bool{}
	for _, s := range stations {
		group := fixed
		switch {
		case group != "":
		case choice == categoryFromTag:
			if tag := stationTag(s, filterTag); tag != "" {
				group = "Radio " + capitalize(tag)
			}
		case choice == categoryFromCountry && s.Country != "":
			group = "Radio " + s.Country
		case choice == categoryFromLanguage && len(s.Languages) > 0:
			group = "Radio " + capitalize(s.Languages[0])
		}
		if group != "" {
			groups[group] = true
		}
		entries = append(entries, store.ImportEntry{Kind: store.KindRadio, Name: s.Name, StreamURL: s.URL,
			LogoURL: httpURL(s.Favicon), Group: group, SourceID: s.UUID})
	}
	res, err := h.store.ImportChannels(r.Context(), entries)
	if err != nil {
		h.serverError(w, err)
		return
	}

	var pkgIDs []int64
	for _, v := range r.Form["package"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			pkgIDs = append(pkgIDs, id)
		}
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	if err := h.store.AddCategoriesToPackages(r.Context(), pkgIDs, store.KindRadio, names); err != nil {
		h.serverError(w, err)
		return
	}

	msg := tr(r, "%d radio stations added, %d updated.", res.Added, res.Updated)
	if missing := len(ids) - len(stations); missing > 0 {
		msg += " " + tr(r, "%d stations are no longer listed by Radio Browser.", missing)
	}
	if len(res.Skipped) > 0 {
		msg += " " + tr(r, "%d skipped: %s", len(res.Skipped), strings.Join(res.Skipped, "; "))
	}
	if len(pkgIDs) > 0 && len(names) == 0 {
		msg += " " + tr(r, "Stations without a category can't be added to a package; choose a category.")
	}
	http.Redirect(w, r, withFlash(back, msg), http.StatusSeeOther)
}

// stationTag is the tag that names the category of a station: the tag that
// was searched for when the station has it, otherwise its first tag.
func stationTag(s radiobrowser.Station, searched string) string {
	for _, t := range s.Tags {
		if searched != "" && strings.EqualFold(t, searched) {
			return t
		}
	}
	if len(s.Tags) > 0 {
		return s.Tags[0]
	}
	return ""
}

// capitalize upper-cases the first letter: Radio Browser writes tags and
// languages in lower case.
func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// httpURL returns s when it is an http(s) URL, otherwise "".
func httpURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}
