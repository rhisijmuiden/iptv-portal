package admin

import (
	"net/http"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/update"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

type infoData struct {
	Version   string
	Status    update.Status
	AutoCheck bool
	DevBuild  bool
	// Prerelease is set when the installed version is a pre-release.
	Prerelease bool
}

func (h *Handler) infoPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	h.renderInfo(w, r, sess, "")
}

func (h *Handler) renderInfo(w http.ResponseWriter, r *http.Request, sess store.Session, errMsg string) {
	st, err := h.store.Settings(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	us := h.updater.Status()
	if errMsg == "" {
		errMsg = us.Error
	}
	h.render(w, r, "info", page{Title: "Info & updates", Nav: "info", Session: &sess, Error: errMsg, Data: infoData{
		Version:    version.Version,
		Status:     us,
		AutoCheck:  st.AutoUpdateCheck,
		DevBuild:   !update.Newer("v999999.0.0", version.Version),
		Prerelease: update.IsPrerelease(version.Version),
	}})
}

func (h *Handler) infoCheck(w http.ResponseWriter, r *http.Request, sess store.Session) {
	st, err := h.updater.Check(r.Context())
	if err != nil {
		h.renderInfo(w, r, sess, err.Error())
		return
	}
	msg := "You are running the latest version."
	if st.Available() {
		msg = tr(r, "A new version is available: %s.", st.Latest().Tag)
	}
	redirectFlash(w, r, "/info", msg)
}

func (h *Handler) infoAuto(w http.ResponseWriter, r *http.Request, sess store.Session) {
	on := r.FormValue("auto") == "on"
	if err := h.store.SetAutoUpdateCheck(r.Context(), on); err != nil {
		h.serverError(w, err)
		return
	}
	msg := "Automatic update check switched off."
	if on {
		msg = "The portal now checks for updates every 24 hours."
	}
	redirectFlash(w, r, "/info", msg)
}

func (h *Handler) infoChannel(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ch := r.FormValue("channel")
	if err := h.store.SetUpdateChannel(r.Context(), ch); err != nil {
		h.renderInfo(w, r, sess, err.Error())
		return
	}
	channel := update.ParseChannel(ch)
	h.updater.SetChannel(channel)
	msg := "Release channel set to stable: only stable releases are offered."
	if channel == update.Test {
		msg = "Release channel set to test: pre-releases are offered as well."
	}
	// Load the releases of the new channel right away.
	if st, err := h.updater.Check(r.Context()); err == nil && st.Available() {
		msg = tr(r, msg) + " " + tr(r, "Version %s is available.", st.Latest().Tag)
	}
	redirectFlash(w, r, "/info", msg)
}

func (h *Handler) infoInstall(w http.ResponseWriter, r *http.Request, sess store.Session) {
	tag := r.FormValue("tag")
	if err := h.updater.Install(r.Context(), tag); err != nil {
		h.log.Warn("update failed", "version", tag, "err", err)
		h.renderInfo(w, r, sess, tr(r, "Update failed: %s", err.Error()))
		return
	}
	h.log.Info("update installed, restarting", "from", version.Version, "to", tag)
	h.render(w, r, "updating", page{Title: "Updating", Nav: "info", Session: &sess,
		Data: map[string]string{"From": version.Version, "To": tag}})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Restart after the page has been sent.
	go func() {
		time.Sleep(time.Second)
		h.restart()
	}()
}
