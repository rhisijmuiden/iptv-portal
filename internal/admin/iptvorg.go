package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/iptvorg"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// defaultCountry is the country shown when the iptv-org page is opened.
const defaultCountry = "NL"

// maxResults caps the rows on the iptv-org page.
const maxResults = 300

// Category choices on the iptv-org page besides an existing category id.
const (
	categoryFromSource  = "@category"
	categoryFromCountry = "@country"
)

type iptvorgRow struct {
	iptvorg.Channel
	CategoryNames string
	Streams       []iptvorgStream
}

type iptvorgStream struct {
	iptvorg.Stream
	Added bool
}

type iptvorgData struct {
	Filter     iptvorg.Filter
	Rows       []iptvorgRow
	More       bool
	Countries  []iptvorg.Country
	Categories []iptvorg.Category
	Loaded     time.Time
	Own        []store.Category
	Packages   []store.Package
	Back       string
}

func (h *Handler) iptvorgPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	p := page{Title: "Browse iptv-org", Nav: "iptvorg", Session: &sess}
	q := r.URL.Query()
	f := iptvorg.Filter{Country: defaultCountry, Category: q.Get("category"), Search: q.Get("q"), Limit: maxResults}
	if q.Has("country") {
		f.Country = q.Get("country")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	cat, err := h.iptvorg.Catalog(ctx, false)
	if err != nil {
		p.Error = "Could not load the iptv-org channel list: " + err.Error()
		h.render(w, r, "iptvorg", p)
		return
	}
	added, err := h.store.StreamURLs(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	own, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	pkgs, err := h.store.Packages(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	data := iptvorgData{Filter: f, Countries: cat.Countries, Categories: cat.Categories, Loaded: cat.Loaded,
		Own: own, Packages: pkgs, Back: r.URL.RequestURI()}
	chs, more := cat.Search(f)
	data.More = more
	for _, ch := range chs {
		row := iptvorgRow{Channel: ch}
		names := make([]string, len(ch.Categories))
		for i, c := range ch.Categories {
			names[i] = cat.CategoryName(c)
		}
		row.CategoryNames = strings.Join(names, ", ")
		for _, s := range ch.Streams {
			row.Streams = append(row.Streams, iptvorgStream{Stream: s, Added: added[s.URL]})
		}
		data.Rows = append(data.Rows, row)
	}
	p.Data = data
	h.render(w, r, "iptvorg", p)
}

func (h *Handler) iptvorgReload(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	back := backTo(r, "/iptv-org")
	if _, err := h.iptvorg.Catalog(ctx, true); err != nil {
		h.render(w, r, "iptvorg", page{Title: "Browse iptv-org", Nav: "iptvorg", Session: &sess,
			Error: "Could not load the iptv-org channel list: " + err.Error()})
		return
	}
	http.Redirect(w, r, withFlash(back, "Channel list reloaded from iptv-org."), http.StatusSeeOther)
}

func (h *Handler) iptvorgAdd(w http.ResponseWriter, r *http.Request, sess store.Session) {
	back := backTo(r, "/iptv-org")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	cat, err := h.iptvorg.Catalog(ctx, false)
	if err != nil {
		http.Redirect(w, r, withFlash(back, "Could not load the iptv-org channel list: "+err.Error()), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	urls := r.Form["stream"]
	if len(urls) == 0 {
		http.Redirect(w, r, withFlash(back, "Select at least one stream to add."), http.StatusSeeOther)
		return
	}

	// Work out the category every stream goes into.
	choice := r.FormValue("target")
	fixed := strings.TrimSpace(r.FormValue("new_category"))
	if fixed == "" && choice != categoryFromSource && choice != categoryFromCountry && choice != "" {
		id, _ := strconv.ParseInt(choice, 10, 64)
		own, err := h.store.Categories(r.Context())
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
	countries := map[string]string{}
	for _, c := range cat.Countries {
		countries[c.Code] = c.Name
	}

	type pick struct {
		ch iptvorg.Channel
		s  iptvorg.Stream
	}
	var picks []pick
	perChannel := map[string]int{}
	seen := map[string]bool{}
	for _, u := range urls {
		ch, s, ok := cat.Find(u)
		if !ok || seen[u] {
			continue
		}
		seen[u] = true
		picks = append(picks, pick{ch, s})
		perChannel[ch.ID+"\x00"+ch.Name]++
	}

	var entries []store.ImportEntry
	groups := map[string]bool{}
	for _, p := range picks {
		name := p.ch.Name
		if perChannel[p.ch.ID+"\x00"+p.ch.Name] > 1 {
			// Tell several streams of one channel apart.
			if extra := strings.TrimSpace(strings.Join(nonEmpty(p.s.Feed, p.s.Quality), " ")); extra != "" {
				name += " (" + extra + ")"
			}
		}
		group := fixed
		switch {
		case group != "":
		case choice == categoryFromCountry:
			group = countries[p.ch.Country]
		case choice == categoryFromSource && len(p.ch.Categories) > 0:
			group = cat.CategoryName(p.ch.Categories[0])
		}
		if group != "" {
			groups[group] = true
		}
		entries = append(entries, store.ImportEntry{Name: name, StreamURL: p.s.URL, LogoURL: p.ch.Logo,
			EPGID: p.ch.EPGID(p.s), Group: group})
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
	if err := h.store.AddCategoriesToPackages(r.Context(), pkgIDs, names); err != nil {
		h.serverError(w, err)
		return
	}

	msg := fmt.Sprintf("%d channels added, %d updated.", res.Added, res.Updated)
	if len(res.Skipped) > 0 {
		msg += fmt.Sprintf(" %d skipped: %s", len(res.Skipped), strings.Join(res.Skipped, "; "))
	}
	if len(pkgIDs) > 0 && len(names) == 0 {
		msg += " Channels without a category can't be added to a package; choose a category."
	}
	http.Redirect(w, r, withFlash(back, msg), http.StatusSeeOther)
}

func nonEmpty(v ...string) []string {
	var out []string
	for _, s := range v {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// withFlash adds a flash message to a local URL that may have a query.
func withFlash(path, msg string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	q := u.Query()
	q.Set("ok", msg)
	u.RawQuery = q.Encode()
	return u.String()
}
