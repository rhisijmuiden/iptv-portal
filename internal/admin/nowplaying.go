package admin

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/nowplaying"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// nowPlayingReads is how many streams the radio page reads at once.
const nowPlayingReads = 6

type nowPlayingJSON struct {
	Title string `json:"title,omitempty"`
	// Note explains an empty title, e.g. that the stream sends nothing.
	Note string `json:"note,omitempty"`
}

// radioNowPlaying answers GET /radio/now-playing?id=1&id=2 with what the
// stations play, for the radio page to fill in after it has loaded.
func (h *Handler) radioNowPlaying(w http.ResponseWriter, r *http.Request, sess store.Session) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	out := map[string]nowPlayingJSON{}
	if !st.RadioNowPlaying {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ids := r.URL.Query()["id"]
	if len(ids) > 200 {
		ids = ids[:200]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, nowPlayingReads)
	for _, s := range ids {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			continue
		}
		c, err := h.store.Channel(ctx, id)
		if err != nil || !c.IsRadio() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info := h.nowPlaying.Get(ctx, c.ID, c.StreamURL)
			res := nowPlayingJSON{Title: info.Title}
			if res.Title == "" {
				res.Note = tr(r, "The stream does not say what is playing.")
				if info.Err != "" && info.Err != nowplaying.ErrNoMetadata.Error() {
					res.Note = tr(r, "Could not read what is playing: %s", info.Err)
				}
			}
			mu.Lock()
			out[s] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, out)
}

// radioNowPlayingSave switches reading now playing information on or off.
func (h *Handler) radioNowPlayingSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	on := r.FormValue("on") == "on"
	if err := h.store.SetRadioNowPlaying(r.Context(), on); err != nil {
		h.serverError(w, err)
		return
	}
	msg := "Players no longer show what radio stations are playing."
	if on {
		msg = "Players now show what radio stations are playing."
	}
	redirectFlash(w, r, "/radio", msg)
}

// radioLabelSave switches the "Radio | " label in front of radio
// categories in the players on or off.
func (h *Handler) radioLabelSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	on := r.FormValue("on") == "on"
	if err := h.store.SetRadioLabel(r.Context(), on); err != nil {
		h.serverError(w, err)
		return
	}
	msg := "Radio categories no longer get a label in the players."
	if on {
		msg = `Radio categories now start with "Radio |" in the players.`
	}
	redirectFlash(w, r, "/radio", msg)
}

// radioLogo shows the logo players get for a station.
func (h *Handler) radioLogo(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, _ := pathID(r)
	c, err := h.store.Channel(r.Context(), id)
	if err != nil || !c.IsRadio() {
		http.NotFound(w, r)
		return
	}
	h.logos.Serve(w, r, c.Name, c.LogoURL)
}
