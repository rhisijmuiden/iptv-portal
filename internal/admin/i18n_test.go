package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/server"
	"github.com/rhisijmuiden/iptv-portal/internal/update"
)

type fakeListeners struct{}

func (fakeListeners) Start(int, int) {}
func (fakeListeners) Status() []server.Status {
	return []server.Status{{Name: "admin panel", Port: 8080, Running: true}, {Name: "portal", Port: 80}}
}
func (fakeListeners) PortAvailable(int) error { return nil }

// TestDutchPages opens every page of the panel in Dutch: each must render
// without template errors and show the Dutch menu.
func TestDutchPages(t *testing.T) {
	h, st := newTestHandler(t, "")
	h.listeners = fakeListeners{}
	h.updater = update.New("http://127.0.0.1:1", "v1.0.0")
	h.guide = epg.New(slog.New(slog.DiscardHandler), t.TempDir())
	ctx := context.Background()
	a, err := st.CreateAdmin(ctx, "admin", "secret-password", false)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path, lang string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.Token})
		if lang != "" {
			r.AddCookie(&http.Cookie{Name: i18n.Cookie, Value: lang})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/", "/channels", "/channels/new", "/categories", "/epg", "/health", "/media", "/media/new",
		"/import", "/users", "/users/new", "/packages", "/packages/new", "/invites", "/settings", "/backups", "/info", "/password"} {
		w := get(path, "nl")
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, w.Code)
			continue
		}
		for _, want := range []string{`<html lang="nl">`, ">Zenders</a>", ">Instellingen</a>", `<option value="nl" selected>Nederlands`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s in Dutch is missing %q", path, want)
			}
		}
		if strings.Contains(body, "%!") {
			t.Errorf("%s has a formatting error: %s", path, body[strings.Index(body, "%!"):][:40])
		}
	}
	if body := get("/channels", "").Body.String(); !strings.Contains(body, ">Channels</a>") || !strings.Contains(body, `lang="en"`) {
		t.Error("without a language cookie the panel is not in English")
	}
	// The default language in the settings applies until an admin picks one.
	s, _ := st.Settings(ctx)
	s.Language = i18n.Dutch
	if err := st.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if body := get("/channels", "").Body.String(); !strings.Contains(body, ">Zenders</a>") {
		t.Error("the default language Dutch is not used")
	}
	if body := get("/channels", "en").Body.String(); !strings.Contains(body, ">Channels</a>") {
		t.Error("the picked language does not win over the default")
	}
}

func TestSetLanguage(t *testing.T) {
	h, _ := newTestHandler(t, "/portal-admin")
	for _, tc := range []struct{ query, cookie, location string }{
		{"lang=nl&next=/users%3Fq%3Djan", "nl", "/portal-admin/users?q=jan"},
		{"lang=en&next=//evil.example", "en", "/portal-admin/"},
		{"lang=xx&next=/info", "", "/portal-admin/info"},
	} {
		w := do(h, "GET", "/portal-admin/language?"+tc.query, "", "")
		if got := w.Header().Get("Location"); w.Code != http.StatusSeeOther || got != tc.location {
			t.Errorf("%s: %d to %q, want %q", tc.query, w.Code, got, tc.location)
		}
		var lang string
		for _, c := range w.Result().Cookies() {
			if c.Name == i18n.Cookie {
				lang = c.Value
				if c.Path != "/portal-admin" {
					t.Errorf("%s: cookie path %q", tc.query, c.Path)
				}
			}
		}
		if lang != tc.cookie {
			t.Errorf("%s: language cookie %q, want %q", tc.query, lang, tc.cookie)
		}
	}
}

func TestDutchFlash(t *testing.T) {
	h, st := newTestHandler(t, "")
	ctx := context.Background()
	a, _ := st.CreateAdmin(ctx, "admin", "secret-password", false)
	sess, _ := st.CreateSession(ctx, a)
	r := httptest.NewRequest("GET", "/settings?ok=Settings+saved.&err=name+must+not+be+empty", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.Token})
	r.AddCookie(&http.Cookie{Name: i18n.Cookie, Value: "nl"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{"Instellingen opgeslagen.", "De naam mag niet leeg zijn.", `name="next" value="/settings"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page is missing %q", want)
		}
	}
}
