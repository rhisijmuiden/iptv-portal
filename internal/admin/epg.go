package admin

import (
	"net/http"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// refreshGuide asks the guide to reload without waiting for it.
func (h *Handler) refreshGuide() {
	select {
	case h.refresh <- struct{}{}:
	default: // a refresh is already queued
	}
}

type epgChannel struct {
	Channel store.Channel
	HasEPG  bool
}

type epgData struct {
	URLs     string
	Status   epg.Status
	Channels []epgChannel
	Matched  int
	NoID     int
}

func (h *Handler) epgPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.renderEPG(w, r, sess, "", "")
}

func (h *Handler) renderEPG(w http.ResponseWriter, r *http.Request, sess store.Session, urls, errMsg string) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	chs, err := h.store.Channels(r.Context(), store.ChannelFilter{})
	if err != nil {
		h.serverError(w, err)
		return
	}
	d := epgData{URLs: urls, Status: h.guide.Status()}
	if errMsg == "" {
		d.URLs = strings.Join(st.EPGURLs, "\n")
	}
	for _, c := range chs {
		ec := epgChannel{Channel: c, HasEPG: c.EPGID != "" && h.guide.Has(c.EPGID)}
		switch {
		case c.EPGID == "":
			d.NoID++
		case ec.HasEPG:
			d.Matched++
		}
		d.Channels = append(d.Channels, ec)
	}
	h.render(w, r, "epg", page{Title: "Program guide", Nav: "epg", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) epgSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	raw := r.FormValue("urls")
	if err := h.store.SetEPGURLs(r.Context(), strings.Fields(raw)); err != nil {
		h.renderEPG(w, r, sess, raw, err.Error())
		return
	}
	h.refreshGuide()
	redirectFlash(w, r, "/epg", "Guide sources saved. The guide is being refreshed; reload this page in a moment.")
}

func (h *Handler) epgRefresh(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.refreshGuide()
	redirectFlash(w, r, "/epg", "The guide is being refreshed; reload this page in a moment.")
}
