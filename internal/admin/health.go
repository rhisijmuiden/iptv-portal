package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/health"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/suggest"
)

// ---- stream health ----

type healthData struct {
	Settings store.HealthSettings
	Status   health.Status
	Took     string // duration of the last run, "" when under a second
	Now      time.Time
	OK       int
	Failing  []healthRow
	Unknown  int
	Disabled int // channels the checker switched off
	Min, Max int // interval limits in minutes
	MaxAfter int
	// Search is the progress of the search for replacements; Suggested
	// the failing channels with a suggestion.
	Search    suggest.Status
	Suggested []healthRow
	AutoAfter int
}

type healthRow struct {
	Channel     store.Channel
	Health      store.ChannelHealth
	Suggestions []store.Suggestion
	// SearchedAt is when a replacement was last looked for; nil when not
	// since the stream failed.
	SearchedAt *time.Time
}

func (h *Handler) healthPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	hs, err := h.store.HealthSettings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.renderHealth(w, r, sess, hs, "")
}

func (h *Handler) renderHealth(w http.ResponseWriter, r *http.Request, sess store.Session, hs store.HealthSettings, errMsg string) {
	chs, err := h.store.Channels(r.Context(), store.ChannelFilter{})
	if err != nil {
		h.serverError(w, err)
		return
	}
	all, err := h.store.ChannelHealth(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	sugs, err := h.store.Suggestions(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	searched, err := h.store.SearchedAt(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	d := healthData{Settings: hs, Status: h.health.Status(), Now: time.Now(),
		Min: store.MinHealthInterval, Max: store.MaxHealthInterval, MaxAfter: store.MaxDisableAfter,
		Search: h.suggest.Status(), AutoAfter: suggest.AutoAfter}
	if d.Status.Duration >= time.Second {
		d.Took = d.Status.Duration.Round(time.Second).String()
	}
	for _, c := range chs {
		hl, ok := all[c.ID]
		switch {
		case !ok:
			d.Unknown++
		case hl.OK:
			d.OK++
		default:
			row := healthRow{Channel: c, Health: hl, Suggestions: sugs[c.ID]}
			if t, ok := searched[c.ID]; ok {
				row.SearchedAt = &t
			}
			d.Failing = append(d.Failing, row)
			if len(row.Suggestions) > 0 {
				d.Suggested = append(d.Suggested, row)
			}
		}
		if ok && hl.AutoDisabled {
			d.Disabled++
		}
	}
	h.render(w, r, "health", page{Title: "Stream health", Nav: "health", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) healthSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var hs store.HealthSettings
	if r.FormValue("enabled") == "on" {
		hs.IntervalMinutes, _ = strconv.Atoi(r.FormValue("interval"))
	}
	hs.Suggest = r.FormValue("suggest") == "on"
	if r.FormValue("auto_disable") == "on" {
		hs.DisableAfter, _ = strconv.Atoi(r.FormValue("disable_after"))
		if hs.DisableAfter == 0 {
			hs.DisableAfter = -1 // switched on without a number: let Validate complain
		}
	}
	if err := h.store.SaveHealthSettings(r.Context(), hs); err != nil {
		h.renderHealth(w, r, sess, hs, err.Error())
		return
	}
	redirectFlash(w, r, "/health", "Stream check settings saved.")
}

func (h *Handler) healthRun(w http.ResponseWriter, r *http.Request, sess store.Session) {
	msg := "Checking all channels. Reload this page to see the results."
	if !h.health.Start() {
		msg = "A check is already running."
	}
	redirectFlash(w, r, backTo(r, "/health"), msg)
}

func (h *Handler) channelCheck(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, err := h.store.Channel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	hl, err := h.health.CheckChannel(r.Context(), c)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !hl.OK {
		redirectError(w, r, backTo(r, "/channels"), tr(r, "%s does not play: %s", c.Name, tr(r, hl.Message)))
		return
	}
	redirectFlash(w, r, backTo(r, "/channels"), tr(r, "%s works: %s.", c.Name, tr(r, hl.Message)))
}

// ---- replacements for failing streams ----

func (h *Handler) suggestRun(w http.ResponseWriter, r *http.Request, sess store.Session) {
	msg := "Looking for replacements of every failing stream. Reload this page to see the results."
	if !h.suggest.Start() {
		msg = "A search is already running."
	}
	redirectFlash(w, r, backTo(r, "/health"), msg)
}

func (h *Handler) channelSuggest(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, err := h.store.Channel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	back := backTo(r, "/health")
	n, err := h.suggest.Channel(r.Context(), c)
	if err != nil {
		redirectError(w, r, back, tr(r, "Could not look for a replacement of %s: %s", c.Name, err.Error()))
		return
	}
	sugs, err := h.store.Suggestions(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	switch {
	case len(sugs[c.ID]) == 0:
		redirectError(w, r, back, tr(r, "No working replacement found for %s.", c.Name))
	case n == 0:
		redirectFlash(w, r, back+"#ch-"+strconv.FormatInt(c.ID, 10), tr(r, "No new replacements found for %s.", c.Name))
	default:
		redirectFlash(w, r, back+"#ch-"+strconv.FormatInt(c.ID, 10), tr(r, "Found %d working replacements for %s.", n, c.Name))
	}
}

func (h *Handler) suggestionAccept(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := backTo(r, "/health")
	c, err := h.store.AcceptSuggestion(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		redirectError(w, r, back, "This suggestion no longer exists.")
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.log.Info("channel switched to a suggested stream", "channel", c.Name, "url", c.StreamURL)
	// Check right away: a channel the checker switched off comes back on.
	hl, err := h.health.CheckChannel(r.Context(), c)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !hl.OK {
		redirectError(w, r, back, tr(r, "%s now uses the new stream, but it does not play: %s", c.Name, tr(r, hl.Message)))
		return
	}
	redirectFlash(w, r, back, tr(r, "%s now uses the new stream and works.", c.Name))
}

func (h *Handler) suggestionDismiss(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := backTo(r, "/health")
	sg, err := h.store.DismissSuggestion(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		redirectError(w, r, back, "This suggestion no longer exists.")
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	back += "#ch-" + strconv.FormatInt(sg.ChannelID, 10)
	redirectFlash(w, r, back, "Suggestion dismissed. It will not be suggested again.")
}
