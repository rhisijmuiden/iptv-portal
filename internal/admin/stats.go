package admin

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/stats"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// ---- viewing statistics ----

// statsPeriods are the periods the statistics page offers, in days.
var statsPeriods = []int{7, 30, 90, 365}

// statsTop is how many channels the page lists before "show all".
const statsTop = 25

type statsData struct {
	Days    int
	Kind    string // "", store.KindTV, store.KindRadio or store.KindVOD
	Periods []int
	// User or Channel is set when the page shows one user or channel.
	User    *store.User
	Channel *store.Channel

	Summary   store.ViewSummary
	Chart     []statsBar
	Channels  []store.ChannelViews
	More      int // channels left out of Channels
	Users     []store.UserViews
	Unwatched []store.UnwatchedChannel
	// Failing and Suggested tell which unwatched channels have a stream
	// that fails, and which of those have a suggested replacement.
	Failing   map[int64]bool
	Suggested map[int64]bool
	Recent    []store.View

	Retention int
	Assume    int // minutes a view is assumed to last
	Now       time.Time
	// Query holds the period and kind for links that keep them.
	Query template.URL
	// Back is this page, to come back to after a change.
	Back string
}

type statsBar struct {
	store.DayViews
	Height int // percent of the highest bar
}

// statsQuery is the query string of the statistics page for these filters.
func statsQuery(days int, kind string) template.URL {
	q := url.Values{"days": {strconv.Itoa(days)}}
	if kind != "" {
		q.Set("kind", kind)
	}
	return template.URL(q.Encode())
}

func (h *Handler) statsPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ctx := r.Context()
	d := statsData{Days: 30, Periods: statsPeriods, Assume: int(stats.Assume / time.Minute), Now: time.Now(), Back: pagePath(r)}
	if n, err := strconv.Atoi(r.FormValue("days")); err == nil {
		for _, p := range statsPeriods {
			if n == p {
				d.Days = n
			}
		}
	}
	switch k := r.FormValue("kind"); k {
	case store.KindTV, store.KindRadio, store.KindVOD:
		d.Kind = k
	}
	d.Query = statsQuery(d.Days, d.Kind)
	today := time.Date(d.Now.Year(), d.Now.Month(), d.Now.Day(), 0, 0, 0, 0, time.Local)
	f := store.ViewFilter{Since: today.AddDate(0, 0, 1-d.Days), Kind: d.Kind}

	if id, err := strconv.ParseInt(r.FormValue("user"), 10, 64); err == nil && id > 0 {
		u, err := h.store.User(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		d.User, f.UserID = &u, id
	}
	if id, err := strconv.ParseInt(r.FormValue("channel"), 10, 64); err == nil && id > 0 {
		c, err := h.store.Channel(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		d.Channel, f.ChannelID, f.Kind, d.Kind = &c, id, "", ""
	}

	var err error
	if d.Summary, err = h.store.ViewSummary(ctx, f); err != nil {
		h.serverError(w, err)
		return
	}
	days, err := h.store.ViewsByDay(ctx, f, d.Now)
	if err != nil {
		h.serverError(w, err)
		return
	}
	highest := 0
	for _, day := range days {
		highest = max(highest, day.Views)
	}
	for _, day := range days {
		b := statsBar{DayViews: day}
		if highest > 0 {
			b.Height = day.Views * 100 / highest
		}
		d.Chart = append(d.Chart, b)
	}
	if d.Channel == nil {
		if d.Channels, err = h.store.ChannelViews(ctx, f); err != nil {
			h.serverError(w, err)
			return
		}
		if len(d.Channels) > statsTop && r.FormValue("all") == "" {
			d.More = len(d.Channels) - statsTop
			d.Channels = d.Channels[:statsTop]
		}
	}
	if d.User == nil {
		if d.Users, err = h.store.UserViews(ctx, f); err != nil {
			h.serverError(w, err)
			return
		}
	}
	if d.User != nil || d.Channel != nil {
		if d.Recent, err = h.store.RecentViews(ctx, f, 50); err != nil {
			h.serverError(w, err)
			return
		}
	} else if d.Kind != store.KindVOD {
		if d.Unwatched, err = h.store.UnwatchedChannels(ctx, d.Kind, f.Since); err != nil {
			h.serverError(w, err)
			return
		}
		hl, err := h.store.ChannelHealth(ctx)
		if err != nil {
			h.serverError(w, err)
			return
		}
		sugs, err := h.store.Suggestions(ctx)
		if err != nil {
			h.serverError(w, err)
			return
		}
		d.Failing, d.Suggested = map[int64]bool{}, map[int64]bool{}
		for _, c := range d.Unwatched {
			if res, ok := hl[c.ID]; ok && !res.OK {
				d.Failing[c.ID] = true
				d.Suggested[c.ID] = len(sugs[c.ID]) > 0
			}
		}
	}
	if d.Retention, err = h.store.ViewRetention(ctx); err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "stats", page{Title: "Viewing statistics", Nav: "stats", Session: &sess, Data: d})
}

func (h *Handler) statsSettings(w http.ResponseWriter, r *http.Request, sess store.Session) {
	days, err := strconv.Atoi(r.FormValue("days"))
	if err != nil {
		days = -1 // let SetViewRetention explain the limits
	}
	if err := h.store.SetViewRetention(r.Context(), days); err != nil {
		redirectError(w, r, backTo(r, "/stats"), err.Error())
		return
	}
	if err := h.stats.Purge(r.Context()); err != nil {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, backTo(r, "/stats"), "Statistics settings saved.")
}

func (h *Handler) statsClear(w http.ResponseWriter, r *http.Request, sess store.Session) {
	if err := h.store.ClearViews(r.Context()); err != nil {
		h.serverError(w, err)
		return
	}
	h.stats.Forget()
	h.log.Info("viewing statistics cleared", "admin", sess.Admin.Username)
	redirectFlash(w, r, "/stats", "All viewing statistics have been removed.")
}
