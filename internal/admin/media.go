package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// refreshMediaNow asks the On Demand catalog to reload without waiting.
func (h *Handler) refreshMediaNow() {
	select {
	case h.refreshMedia <- struct{}{}:
	default: // a refresh is already queued
	}
}

type mediaRow struct {
	Server store.MediaServer
	Status media.ServerStatus
}

type mediaListData struct {
	Servers    []mediaRow
	Refreshing bool
	// Packages is the number of packages that include On Demand.
	Packages int
}

func (h *Handler) mediaList(w http.ResponseWriter, r *http.Request, sess store.Session) {
	servers, err := h.store.MediaServers(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	pkgs, err := h.store.Packages(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	d := mediaListData{Refreshing: h.media.Refreshing()}
	for _, p := range pkgs {
		if p.OnDemand {
			d.Packages++
		}
	}
	for _, s := range servers {
		d.Servers = append(d.Servers, mediaRow{Server: s, Status: h.media.Status(s.ID)})
	}
	h.render(w, r, "media", page{Title: "On Demand", Nav: "media", Session: &sess, Data: d})
}

type mediaFormData struct {
	Server    store.MediaServer
	Libraries []media.Library
	// LibraryError says why the libraries could not be listed.
	LibraryError string
}

// libraries asks a media server for its movie and series libraries.
func libraries(ctx context.Context, s store.MediaServer) ([]media.Library, error) {
	cl, err := media.NewClient(s, nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	libs, err := cl.Libraries(ctx)
	if err == nil && len(libs) == 0 {
		err = errors.New("the server has no movie or series libraries")
	}
	return libs, err
}

func (h *Handler) mediaForm(w http.ResponseWriter, r *http.Request, sess store.Session) {
	s := store.MediaServer{Kind: store.MediaJellyfin, Enabled: true}
	if id, ok := pathID(r); ok {
		var err error
		s, err = h.store.MediaServer(r.Context(), id)
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
	h.renderMediaForm(w, r, sess, s, "")
}

func (h *Handler) renderMediaForm(w http.ResponseWriter, r *http.Request, sess store.Session, s store.MediaServer, errMsg string) {
	d := mediaFormData{Server: s}
	if s.ID != 0 {
		stored, err := h.store.MediaServer(r.Context(), s.ID)
		if err == nil {
			libs, err := libraries(r.Context(), stored)
			if err != nil {
				d.LibraryError = err.Error()
			}
			d.Libraries = libs
		}
	}
	d.Server.Token = "" // never sent back to the browser
	title := "Add media server"
	if s.ID != 0 {
		title = "Edit " + s.Name
	}
	h.render(w, r, "mediaserver", page{Title: title, Nav: "media", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) mediaSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	s := store.MediaServer{
		Name:    r.FormValue("name"),
		Kind:    r.FormValue("kind"),
		URL:     r.FormValue("url"),
		Token:   r.FormValue("token"),
		Direct:  r.FormValue("direct") == "1",
		Enabled: r.FormValue("enabled") == "on",
	}
	if id, ok := pathID(r); ok {
		s.ID = id
	} else if r.PathValue("id") != "" {
		http.NotFound(w, r)
		return
	}

	msg := fmt.Sprintf("Media server %q saved. The libraries are being read; reload this page in a moment.", s.Name)
	if s.ID == 0 {
		// Check the connection first and offer every library to start with.
		if err := s.Validate(); err != nil {
			h.renderMediaForm(w, r, sess, s, err.Error())
			return
		}
		libs, err := libraries(r.Context(), s)
		if err != nil {
			h.renderMediaForm(w, r, sess, s, "Could not read the libraries: "+err.Error())
			return
		}
		for _, l := range libs {
			s.Libraries = append(s.Libraries, l.ID)
		}
	} else {
		old, err := h.store.MediaServer(r.Context(), s.ID)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			h.serverError(w, err)
			return
		}
		s.Libraries = old.Libraries
		// The checkboxes are only on the page when the server could be
		// reached; otherwise the stored choice stays.
		if r.FormValue("libraries_shown") == "1" {
			s.Libraries = r.Form["library"]
		}
	}
	id, err := h.store.SaveMediaServer(r.Context(), s)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.renderMediaForm(w, r, sess, s, err.Error())
		return
	}
	h.refreshMediaNow()
	if s.ID == 0 {
		redirectFlash(w, r, fmt.Sprintf("/media/%d", id),
			fmt.Sprintf("Media server %q added with all its libraries. Untick the libraries you do not want to offer.", s.Name))
		return
	}
	redirectFlash(w, r, "/media", msg)
}

func (h *Handler) mediaDelete(w http.ResponseWriter, r *http.Request, sess store.Session) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.store.DeleteMediaServer(r.Context(), id); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.serverError(w, err)
		return
	}
	h.refreshMediaNow()
	redirectFlash(w, r, "/media", "Media server deleted.")
}

func (h *Handler) mediaRefresh(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.refreshMediaNow()
	redirectFlash(w, r, "/media", "The media servers are being read again; reload this page in a moment.")
}
