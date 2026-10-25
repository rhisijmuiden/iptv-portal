package portal

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/logos"
	"github.com/rhisijmuiden/iptv-portal/internal/nowplaying"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Radio stations rarely have a guide of their own, so the portal makes one:
// every station gets an EPG id, an hourly schedule with the station's name
// and, in the hour that is on now, the song the stream says is playing.
// Logos are served by the portal (see package logos), so every station has
// one the player can show.

// guideHours is how far ahead the generated radio schedule goes.
const guideHours = 24

// nowPlayingWait is the longest a guide request waits for a stream.
const nowPlayingWait = 5 * time.Second

// SetRadio replaces the now playing reader and logo cache, so the admin
// panel and the portal share them.
func (h *Handler) SetRadio(np *nowplaying.Service, lc *logos.Cache) {
	h.nowPlaying, h.logos = np, lc
}

// epgID is the guide id of a channel: its own EPG ID, or for a radio
// station without one a generated id.
func epgID(c store.Channel) string {
	if c.EPGID == "" && c.IsRadio() {
		return "radio" + strconv.FormatInt(c.ID, 10) + ".iptv-portal"
	}
	return c.EPGID
}

// logoURL is the logo handed to players: radio stations get the portal's
// converted (or generated) logo, TV channels their own.
func logoURL(base string, c store.Channel) string {
	if c.IsRadio() {
		return base + "/logo/" + strconv.FormatInt(c.ID, 10) + ".png"
	}
	return c.LogoURL
}

// radioGuide reports whether the portal makes the schedule of c, i.e. it
// is a radio station that is not in a downloaded guide.
func (h *Handler) radioGuide(c store.Channel) bool {
	return c.IsRadio() && (c.EPGID == "" || !h.guide.Has(c.EPGID))
}

// radioProgrammes makes the schedule of a radio station from the hour
// before now to guideHours ahead. The block that is on now shows info's
// title when known.
func radioProgrammes(lang string, c store.Channel, info nowplaying.Info, now time.Time) []epg.Programme {
	id := epgID(c)
	desc := strings.TrimSpace(info.Description)
	if desc == "" {
		desc = i18n.Tf(lang, "Live radio from %s.", c.Name)
	}
	if info.Genre != "" {
		desc += " " + i18n.Tf(lang, "Genre: %s.", info.Genre)
	}
	hour := now.Truncate(time.Hour)
	out := make([]epg.Programme, 0, guideHours+1)
	for i := -1; i < guideHours; i++ {
		start := hour.Add(time.Duration(i) * time.Hour)
		p := epg.Programme{Channel: id, Start: start, Stop: start.Add(time.Hour), Title: c.Name, Desc: desc}
		if i == 0 && info.Title != "" {
			p.Title = info.Title
			p.Desc = i18n.Tf(lang, "Now playing on %s.", c.Name)
			if info.Artist != "" {
				p.Desc = i18n.Tf(lang, "Now playing on %s: %s by %s.", c.Name, info.Song, info.Artist)
			}
		}
		out = append(out, p)
	}
	return out
}

// currentSong returns what a station plays. With wait it reads the stream
// when the cached result is old; otherwise only the cache is used.
func (h *Handler) currentSong(ctx context.Context, st store.Settings, c store.Channel, wait bool) nowplaying.Info {
	if !st.RadioNowPlaying {
		return nowplaying.Info{}
	}
	if !wait {
		info, _ := h.nowPlaying.Cached(c.ID)
		if time.Since(info.Checked) > 10*time.Minute {
			return nowplaying.Info{}
		}
		return info
	}
	ctx, cancel := context.WithTimeout(ctx, nowPlayingWait)
	defer cancel()
	return h.nowPlaying.Get(ctx, c.ID, c.StreamURL)
}

// logo serves /logo/{id}.png for enabled radio stations. It needs no
// login: players fetch logos without credentials.
func (h *Handler) logo(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	id, err := strconv.ParseInt(strings.TrimSuffix(file, path.Ext(file)), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c, err := h.store.Channel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (!c.IsRadio() || !c.Enabled)) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.logos.Serve(w, r, c.Name, c.LogoURL)
}
