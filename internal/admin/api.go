package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// The JSON API under /api/v1/ is what Build My TV Station uses to manage
// the portal (see the station contract). It only accepts the service token;
// without a configured token every request is refused.

// requireToken checks "Authorization: Bearer <token>" against the service
// token. No cookie is involved, so there is no CSRF check.
func (h *Handler) requireToken(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st, err := h.store.Settings(r.Context())
		if err != nil {
			h.apiError(w, http.StatusInternalServerError, err)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if st.ServiceToken == "" || !ok ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(st.ServiceToken)) != 1 {
			if ok && st.ServiceToken != "" {
				h.log.Warn("API request with a wrong service token", "ip", web.ClientIP(r))
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="iptv-portal"`)
			writeJSON(w, http.StatusUnauthorized, apiErrorBody{"a valid service token is required"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		fn(w, r)
	})
}

// apiChannel is a channel as the API reads and writes it.
type apiChannel struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	StreamURL string `json:"streamUrl"`
	LogoURL   string `json:"logoUrl"`
	Category  string `json:"category"`
	EPGID     string `json:"epgId"`
	SortOrder int    `json:"sortOrder"`
	Enabled   bool   `json:"enabled"`
}

func toAPIChannel(c store.Channel) apiChannel {
	return apiChannel{ID: c.ID, Name: c.Name, StreamURL: c.StreamURL, LogoURL: c.LogoURL, Category: c.Category,
		EPGID: c.EPGID, SortOrder: c.SortOrder, Enabled: c.Enabled}
}

// apiChannelInput is the request body of POST and PUT. Enabled is optional:
// a new channel is enabled by default, an updated one keeps its state.
type apiChannelInput struct {
	Name      string `json:"name"`
	StreamURL string `json:"streamUrl"`
	LogoURL   string `json:"logoUrl"`
	Category  string `json:"category"`
	EPGID     string `json:"epgId"`
	SortOrder int    `json:"sortOrder"`
	Enabled   *bool  `json:"enabled"`
}

type apiErrorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) apiError(w http.ResponseWriter, status int, err error) {
	msg := err.Error()
	if status >= 500 {
		h.log.Error("internal error", "err", err)
		msg = "internal error, see the server log"
	}
	writeJSON(w, status, apiErrorBody{msg})
}

func (h *Handler) apiChannelList(w http.ResponseWriter, r *http.Request) {
	// The API manages TV channels only; radio stations are left out.
	chs, err := h.store.Channels(r.Context(), store.ChannelFilter{Kind: store.KindTV})
	if err != nil {
		h.apiError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]apiChannel, 0, len(chs))
	for _, c := range chs {
		out = append(out, toAPIChannel(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) apiChannelCreate(w http.ResponseWriter, r *http.Request) {
	h.apiChannelSave(w, r, store.Channel{Enabled: true}, http.StatusCreated)
}

func (h *Handler) apiChannelUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.apiError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	c, err := h.store.Channel(r.Context(), id)
	if err == nil && c.IsRadio() {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		h.apiError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		h.apiError(w, http.StatusInternalServerError, err)
		return
	}
	h.apiChannelSave(w, r, c, http.StatusOK)
}

// apiChannelSave applies the request body to c, stores it and answers with
// the stored channel.
func (h *Handler) apiChannelSave(w http.ResponseWriter, r *http.Request, c store.Channel, status int) {
	var in apiChannelInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		h.apiError(w, http.StatusBadRequest, errors.New("invalid JSON: "+err.Error()))
		return
	}
	c.Name, c.StreamURL, c.LogoURL, c.EPGID, c.SortOrder = in.Name, in.StreamURL, in.LogoURL, in.EPGID, in.SortOrder
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	// Validate before creating the category, so a rejected channel leaves
	// no new category behind.
	if err := c.Validate(); err != nil {
		h.apiError(w, http.StatusBadRequest, err)
		return
	}
	cat, err := h.store.EnsureCategory(r.Context(), c.Kind, in.Category)
	if err != nil {
		h.apiError(w, http.StatusInternalServerError, err)
		return
	}
	c.CategoryID = cat
	id, err := h.store.SaveChannel(r.Context(), c)
	if errors.Is(err, store.ErrNotFound) {
		h.apiError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		h.apiError(w, http.StatusInternalServerError, err)
		return
	}
	saved, err := h.store.Channel(r.Context(), id)
	if err != nil {
		h.apiError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, status, toAPIChannel(saved))
}

func (h *Handler) apiChannelDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		h.apiError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	err := h.store.DeleteChannel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		h.apiError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		h.apiError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
