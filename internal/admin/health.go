package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/health"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
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
}

type healthRow struct {
	Channel store.Channel
	Health  store.ChannelHealth
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
	d := healthData{Settings: hs, Status: h.health.Status(), Now: time.Now(),
		Min: store.MinHealthInterval, Max: store.MaxHealthInterval, MaxAfter: store.MaxDisableAfter}
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
			d.Failing = append(d.Failing, healthRow{Channel: c, Health: hl})
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
		redirectError(w, r, backTo(r, "/channels"), fmt.Sprintf("%s does not play: %s", c.Name, hl.Message))
		return
	}
	redirectFlash(w, r, backTo(r, "/channels"), fmt.Sprintf("%s works: %s.", c.Name, hl.Message))
}
