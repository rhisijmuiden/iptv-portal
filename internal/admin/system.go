package admin

import (
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/sysinfo"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

// ---- system resources ----

// systemReport is what the system resources page draws: the current
// status and the history of the chosen range. The page reloads it from
// /system/data every few seconds.
type systemReport struct {
	sysinfo.Status
	Version  string           `json:"version"`
	Range    string           `json:"range"` // "15m", "hour" or "day"
	History  []sysinfo.Sample `json:"history"`
	Interval int              `json:"interval"` // seconds between samples
}

func (h *Handler) systemReport(r *http.Request) systemReport {
	if len(h.system.History(false)) == 0 { // not sampling in the background
		h.system.Sample()
	}
	rep := systemReport{Status: h.system.Status(), Version: version.Version, Range: "hour",
		Interval: int(sysinfo.Interval.Seconds())}
	switch v := r.FormValue("range"); v {
	case "15m", "day":
		rep.Range = v
	}
	rep.History = h.system.History(rep.Range == "day")
	return rep
}

type systemData struct {
	Range string
	// Report is the first report as JSON, so the page draws without
	// waiting for the first refresh.
	Report template.JS
}

func (h *Handler) systemPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	rep := h.systemReport(r)
	b, err := json.Marshal(rep)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "system", page{Title: "System resources", Nav: "system", Session: &sess,
		Data: systemData{Range: rep.Range, Report: template.JS(b)}})
}

func (h *Handler) systemJSON(w http.ResponseWriter, r *http.Request, sess store.Session) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(h.systemReport(r))
}
