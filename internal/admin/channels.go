package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/m3u"
	"github.com/rhisijmuiden/iptv-portal/internal/radiobrowser"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ---- channels ----

// section is the part of the panel a channel belongs to: TV channels
// under /channels or radio stations under /radio. Both share the handlers.
type section struct {
	Kind string // store.KindTV or store.KindRadio
	Path string // "/channels" or "/radio"
	Nav  string
	List string // templates and titles
	Form string
	Name string // "Channels" or "Radio stations"
	New  string // title of the form for a new entry
	Edit string
}

var (
	tvSection = section{Kind: store.KindTV, Path: "/channels", Nav: "channels", List: "channels", Form: "channel",
		Name: "Channels", New: "Add channel", Edit: "Edit channel"}
	radioSection = section{Kind: store.KindRadio, Path: "/radio", Nav: "radio", List: "radios", Form: "radio",
		Name: "Radio stations", New: "Add radio station", Edit: "Edit radio station"}
)

func sectionOf(c store.Channel) section {
	if c.IsRadio() {
		return radioSection
	}
	return tvSection
}

type channelListData struct {
	Channels   []store.Channel
	Categories []store.Category
	Filter     store.ChannelFilter
	Health     map[int64]store.ChannelHealth
	Suggested  map[int64][]store.Suggestion // open replacement suggestions
	HealthOnly string                       // "ok", "failing" or "unchecked"; "" shows all
	Now        time.Time
	Back       string // current list URL, to return to after a toggle
	NowPlaying bool   // radio only: players are shown what stations play
}

func (h *Handler) channelList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.listChannels(w, r, sess, tvSection)
}

func (h *Handler) radioList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.listChannels(w, r, sess, radioSection)
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request, sess store.Session, sec section) {
	f := store.ChannelFilter{Kind: sec.Kind, Search: r.URL.Query().Get("q")}
	f.CategoryID, _ = strconv.ParseInt(r.URL.Query().Get("category"), 10, 64)
	chs, err := h.store.Channels(r.Context(), f)
	if err != nil {
		h.serverError(w, err)
		return
	}
	cats, err := h.store.CategoriesOf(r.Context(), sec.Kind)
	if err != nil {
		h.serverError(w, err)
		return
	}
	hl, err := h.store.ChannelHealth(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	sugs, err := h.store.Suggestions(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	only := r.URL.Query().Get("health")
	if only != "" {
		var keep []store.Channel
		for _, c := range chs {
			res, checked := hl[c.ID]
			state := "unchecked"
			switch {
			case checked && res.OK:
				state = "ok"
			case checked:
				state = "failing"
			}
			if state == only {
				keep = append(keep, c)
			}
		}
		chs = keep
	}
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, sec.List, page{Title: sec.Name, Nav: sec.Nav, Session: &sess,
		Data: channelListData{Channels: chs, Categories: cats, Filter: f, Health: hl, Suggested: sugs, HealthOnly: only,
			Now: time.Now(), Back: r.URL.RequestURI(), NowPlaying: st.RadioNowPlaying}})
}

type channelFormData struct {
	Channel    store.Channel
	Categories []store.Category
}

func (h *Handler) channelForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.showChannelForm(w, r, sess, tvSection)
}

func (h *Handler) radioForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.showChannelForm(w, r, sess, radioSection)
}

func (h *Handler) showChannelForm(w http.ResponseWriter, r *http.Request, sess store.Session, sec section) {
	c := store.Channel{Kind: sec.Kind, Enabled: true}
	if id, ok := pathID(r); ok {
		var err error
		c, err = h.store.Channel(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		if other := sectionOf(c); other.Kind != sec.Kind {
			// A radio station opened as a channel or the other way round.
			http.Redirect(w, r, other.Path+"/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
			return
		}
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	h.renderChannelForm(w, r, sess, c, "")
}

func (h *Handler) renderChannelForm(w http.ResponseWriter, r *http.Request, sess store.Session, c store.Channel, errMsg string) {
	sec := sectionOf(c)
	cats, err := h.store.CategoriesOf(r.Context(), sec.Kind)
	if err != nil {
		h.serverError(w, err)
		return
	}
	title := sec.New
	if c.ID != 0 {
		title = sec.Edit
	}
	h.render(w, r, sec.Form, page{Title: title, Nav: sec.Nav, Session: &sess, Error: errMsg,
		Data: channelFormData{Channel: c, Categories: cats}})
}

func (h *Handler) channelSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.saveChannel(w, r, sess, tvSection)
}

func (h *Handler) radioSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.saveChannel(w, r, sess, radioSection)
}

func (h *Handler) saveChannel(w http.ResponseWriter, r *http.Request, sess store.Session, sec section) {
	c := store.Channel{
		Kind:      sec.Kind,
		Name:      r.FormValue("name"),
		StreamURL: r.FormValue("stream_url"),
		LogoURL:   r.FormValue("logo_url"),
		EPGID:     r.FormValue("epg_id"),
		Enabled:   r.FormValue("enabled") == "on",
	}
	if id, ok := pathID(r); ok {
		old, err := h.store.Channel(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && old.Kind != sec.Kind) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		c.ID = id
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	c.CategoryID, _ = strconv.ParseInt(r.FormValue("category_id"), 10, 64)
	c.SortOrder, _ = strconv.Atoi(r.FormValue("sort_order"))
	if c.IsRadio() {
		// Stations often publish a .pls or .m3u file; players want the
		// stream inside it.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		c.StreamURL = radiobrowser.ResolvePlaylist(ctx, http.DefaultClient, strings.TrimSpace(c.StreamURL))
		cancel()
	}
	if name := strings.TrimSpace(r.FormValue("new_category")); name != "" {
		if err := c.Validate(); err != nil {
			// Check first, so a rejected entry leaves no new category behind.
			h.renderChannelForm(w, r, sess, c, err.Error())
			return
		}
		id, err := h.store.SaveCategory(r.Context(), store.Category{Name: name, Kind: sec.Kind})
		if err != nil {
			h.renderChannelForm(w, r, sess, c, err.Error())
			return
		}
		c.CategoryID = id
	}
	if _, err := h.store.SaveChannel(r.Context(), c); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.renderChannelForm(w, r, sess, c, err.Error())
		return
	}
	msg := "Channel %q saved."
	if c.IsRadio() {
		msg = "Radio station %q saved."
	}
	redirectFlash(w, r, sec.Path, tr(r, msg, strings.TrimSpace(c.Name)))
}

func (h *Handler) channelToggle(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.SetChannelEnabled(r.Context(), id, r.FormValue("enabled") == "1"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		h.serverError(w, err)
		return
	}
	// Switched by hand: the stream check should not switch it back.
	if err := h.store.ClearAutoDisabled(r.Context(), id); err != nil {
		h.serverError(w, err)
		return
	}
	http.Redirect(w, r, backTo(r, "/channels"), http.StatusSeeOther)
}

func (h *Handler) radioDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteChannel(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/radio", "Radio station deleted.")
}

func (h *Handler) channelDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteChannel(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/channels", "Channel deleted.")
}

// backTo returns the "back" form value when it is a local path.
func backTo(r *http.Request, fallback string) string {
	if b := r.FormValue("back"); web.IsLocalPath(b) {
		return b
	}
	return fallback
}

// ---- categories ----

func (h *Handler) categoryList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.renderCategories(w, r, sess, "")
}

func (h *Handler) renderCategories(w http.ResponseWriter, r *http.Request, sess store.Session, errMsg string) {
	cats, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "categories", page{Title: "Categories", Nav: "categories", Session: &sess, Error: errMsg, Data: cats})
}

func (h *Handler) categorySave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	c := store.Category{Name: r.FormValue("name"), Kind: r.FormValue("kind")}
	c.ID, _ = strconv.ParseInt(r.FormValue("id"), 10, 64)
	c.SortOrder, _ = strconv.Atoi(r.FormValue("sort_order"))
	if _, err := h.store.SaveCategory(r.Context(), c); err != nil {
		h.renderCategories(w, r, sess, err.Error())
		return
	}
	redirectFlash(w, r, "/categories", "Category saved.")
}

func (h *Handler) categoryDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteCategory(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	redirectFlash(w, r, "/categories", "Category deleted. Its channels are now uncategorized.")
}

// ---- import ----

func (h *Handler) importForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.render(w, r, "import", page{Title: "Import playlist", Nav: "import", Session: &sess})
}

const maxPlaylistSize = 10 << 20

type importData struct {
	store.ImportResult
	EPGAdded []string
}

func (h *Handler) importRun(w http.ResponseWriter, r *http.Request, sess store.Session) {
	p := page{Title: "Import playlist", Nav: "import", Session: &sess}
	src, err := playlistSource(r)
	if err != nil {
		p.Error = err.Error()
		h.render(w, r, "import", p)
		return
	}
	defer src.Close()
	pl, err := m3u.Parse(io.LimitReader(src, maxPlaylistSize))
	entries := pl.Entries
	if err != nil {
		p.Error = tr(r, "Could not read the playlist: %s", err.Error())
		h.render(w, r, "import", p)
		return
	}
	if len(entries) == 0 {
		p.Error = "No streams found in the playlist."
		h.render(w, r, "import", p)
		return
	}
	in := make([]store.ImportEntry, len(entries))
	for i, e := range entries {
		in[i] = store.ImportEntry{Name: e.Name, StreamURL: e.URL, LogoURL: e.Attr("tvg-logo"),
			EPGID: e.Attr("tvg-id"), Group: e.Attr("group-title")}
		if g := strings.TrimSpace(r.FormValue("category")); g != "" {
			in[i].Group = g
		}
	}
	res, err := h.store.ImportChannels(r.Context(), in)
	if err != nil {
		h.serverError(w, err)
		return
	}
	data := importData{ImportResult: res}
	// A playlist that announces its guide (x-tvg-url) has that guide added
	// as a source, so the EPG works without extra setup.
	for _, u := range pl.EPGURLs() {
		added, err := h.store.AddEPGURL(r.Context(), u)
		if err != nil {
			h.log.Warn("adding guide from playlist", "url", u, "err", err)
			continue
		}
		if added {
			data.EPGAdded = append(data.EPGAdded, u)
		}
	}
	if len(data.EPGAdded) > 0 {
		h.refreshGuide()
	}
	p.Data = data
	h.render(w, r, "import", p)
}

// playlistSource returns the uploaded file, the pasted text or the playlist
// downloaded from the given URL, in that order of preference.
func playlistSource(r *http.Request) (io.ReadCloser, error) {
	if f, _, err := r.FormFile("file"); err == nil {
		return f, nil
	}
	if text := r.FormValue("text"); strings.TrimSpace(text) != "" {
		return io.NopCloser(strings.NewReader(text)), nil
	}
	u := strings.TrimSpace(r.FormValue("url"))
	if u == "" {
		return nil, errors.New("choose a file, paste a playlist or enter a URL")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, errors.New("URL must start with http:// or https://")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("User-Agent", "iptv-portal")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("downloading the playlist failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("downloading the playlist failed: %s", resp.Status)
	}
	return cancelOnClose{resp.Body, cancel}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
