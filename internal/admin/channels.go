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
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ---- channels ----

type channelListData struct {
	Channels   []store.Channel
	Categories []store.Category
	Filter     store.ChannelFilter
	Back       string // current list URL, to return to after a toggle
}

func (h *Handler) channelList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	f := store.ChannelFilter{Search: r.URL.Query().Get("q")}
	f.CategoryID, _ = strconv.ParseInt(r.URL.Query().Get("category"), 10, 64)
	chs, err := h.store.Channels(r.Context(), f)
	if err != nil {
		h.serverError(w, err)
		return
	}
	cats, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "channels", page{Title: "Channels", Nav: "channels", Session: &sess,
		Data: channelListData{Channels: chs, Categories: cats, Filter: f, Back: r.URL.RequestURI()}})
}

type channelFormData struct {
	Channel    store.Channel
	Categories []store.Category
}

func (h *Handler) channelForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	c := store.Channel{Enabled: true}
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
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	h.renderChannelForm(w, r, sess, c, "")
}

func (h *Handler) renderChannelForm(w http.ResponseWriter, r *http.Request, sess store.Session, c store.Channel, errMsg string) {
	cats, err := h.store.Categories(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	title := "Add channel"
	if c.ID != 0 {
		title = "Edit channel"
	}
	h.render(w, r, "channel", page{Title: title, Nav: "channels", Session: &sess, Error: errMsg,
		Data: channelFormData{Channel: c, Categories: cats}})
}

func (h *Handler) channelSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	c := store.Channel{
		Name:      r.FormValue("name"),
		StreamURL: r.FormValue("stream_url"),
		LogoURL:   r.FormValue("logo_url"),
		EPGID:     r.FormValue("epg_id"),
		Enabled:   r.FormValue("enabled") == "on",
	}
	if id, ok := pathID(r); ok {
		c.ID = id
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}
	c.CategoryID, _ = strconv.ParseInt(r.FormValue("category_id"), 10, 64)
	c.SortOrder, _ = strconv.Atoi(r.FormValue("sort_order"))
	if name := strings.TrimSpace(r.FormValue("new_category")); name != "" {
		id, err := h.store.SaveCategory(r.Context(), store.Category{Name: name})
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
	redirectFlash(w, r, "/channels", fmt.Sprintf("Channel %q saved.", strings.TrimSpace(c.Name)))
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
	http.Redirect(w, r, backTo(r, "/channels"), http.StatusSeeOther)
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
	b := r.FormValue("back")
	if strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "//") && !strings.Contains(b, `\`) {
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
	c := store.Category{Name: r.FormValue("name")}
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
	h.render(w, r, "import", page{Title: "Import", Nav: "import", Session: &sess})
}

const maxPlaylistSize = 10 << 20

func (h *Handler) importRun(w http.ResponseWriter, r *http.Request, sess store.Session) {
	p := page{Title: "Import", Nav: "import", Session: &sess}
	src, err := playlistSource(r)
	if err != nil {
		p.Error = err.Error()
		h.render(w, r, "import", p)
		return
	}
	defer src.Close()
	entries, err := m3u.Parse(io.LimitReader(src, maxPlaylistSize))
	if err != nil {
		p.Error = "Could not read the playlist: " + err.Error()
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
	p.Data = res
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
