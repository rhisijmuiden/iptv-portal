package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestHandler(t *testing.T, base string) (*Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h, err := New(Config{Store: st, BasePath: base, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return h, st
}

func setToken(t *testing.T, st *store.Store, token string) {
	t.Helper()
	ctx := context.Background()
	s, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.ServiceToken = token
	if err := st.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAdminHealthz(t *testing.T) {
	for _, tc := range []struct{ base, path string }{
		{"", "/healthz"}, {"/portal-admin", "/healthz"}, {"/portal-admin", "/portal-admin/healthz"},
	} {
		h, _ := newTestHandler(t, tc.base)
		w := do(h, "GET", tc.path, "", "")
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
			t.Fatalf("base %q %s: %d %s", tc.base, tc.path, w.Code, w.Body)
		}
		if got["status"] != "ok" || got["app"] != "iptv-portal" || got["contract"] != 1.0 {
			t.Errorf("base %q %s: %v", tc.base, tc.path, got)
		}
	}
}

func TestAPIToken(t *testing.T) {
	h, st := newTestHandler(t, "")
	// No token configured: Bearer headers get nothing.
	if w := do(h, "GET", "/api/v1/channels", testToken, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("without a configured token: %d", w.Code)
	}
	setToken(t, st, testToken)
	if w := do(h, "GET", "/api/v1/channels", testToken, ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("good token: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/v1/channels", testToken+"x", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := do(h, "GET", "/api/v1/channels", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	// A Bearer request is not a session: the HTML panel still wants a login.
	if w := do(h, "GET", "/channels", testToken, ""); w.Code != http.StatusSeeOther {
		t.Errorf("panel with token: %d", w.Code)
	}
	if err := (&store.Settings{ServiceToken: "short", PortalName: "x", AdminPort: 1, PortalPort: 2}).Validate(); err == nil {
		t.Error("a short token was accepted")
	}
}

func TestAPIChannels(t *testing.T) {
	h, st := newTestHandler(t, "/portal-admin")
	setToken(t, st, testToken)
	ctx := context.Background()

	w := do(h, "POST", "/portal-admin/api/v1/channels", testToken,
		`{"name":"Channel 1","streamUrl":"https://s.example/1.m3u8","logoUrl":"https://s.example/1.png","category":"Local","epgId":"one.nl","sortOrder":3}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var c apiChannel
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.ID == 0 || c.Category != "Local" || !c.Enabled || c.SortOrder != 3 || c.EPGID != "one.nl" {
		t.Errorf("created %+v", c)
	}
	cats, _ := st.Categories(ctx)
	if len(cats) != 1 || cats[0].Name != "Local" {
		t.Errorf("categories %+v", cats)
	}

	if w := do(h, "POST", "/portal-admin/api/v1/channels", testToken, `{"name":"Bad","streamUrl":"ftp://x","category":"Other"}`); w.Code != http.StatusBadRequest {
		t.Errorf("invalid channel: %d", w.Code)
	}
	if cats, _ := st.Categories(ctx); len(cats) != 1 {
		t.Errorf("a rejected channel created a category: %+v", cats)
	}

	id := "/portal-admin/api/v1/channels/" + strconv.FormatInt(c.ID, 10)
	w = do(h, "PUT", id, testToken, `{"name":"Channel One","streamUrl":"https://s.example/1b.m3u8","category":"local","enabled":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.Name != "Channel One" || c.Enabled || c.Category != "Local" || c.LogoURL != "" {
		t.Errorf("updated %+v", c)
	}
	if w := do(h, "PUT", "/portal-admin/api/v1/channels/999", testToken, `{"name":"X","streamUrl":"https://s.example/x"}`); w.Code != http.StatusNotFound {
		t.Errorf("update missing: %d", w.Code)
	}

	if w := do(h, "DELETE", id, testToken, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if w := do(h, "DELETE", id, testToken, ""); w.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", w.Code)
	}
	if chs, _ := st.Channels(ctx, store.ChannelFilter{}); len(chs) != 0 {
		t.Errorf("channels left: %+v", chs)
	}
}

func TestBasePath(t *testing.T) {
	h, st := newTestHandler(t, "/portal-admin")
	if _, err := st.CreateAdmin(context.Background(), "admin", "secret-password", false); err != nil {
		t.Fatal(err)
	}

	w := do(h, "GET", "/portal-admin?x=1", "", "")
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/portal-admin/?x=1" {
		t.Errorf("no trailing slash: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, "GET", "/portal-admin/", "", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/portal-admin/login" {
		t.Errorf("not logged in: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, "GET", "/login", "", ""); w.Code != http.StatusNotFound {
		t.Errorf("root path outside the base path: %d", w.Code)
	}
	if w := do(h, "GET", "/portal-admin/static/style.css", "", ""); w.Code != http.StatusOK {
		t.Errorf("stylesheet: %d", w.Code)
	}
	w = do(h, "GET", "/portal-admin/login", "", "")
	body := w.Body.String()
	for _, want := range []string{`href="/portal-admin/static/style.css"`, `action="/portal-admin/login"`, `src="/portal-admin/static/brand/rhis-icon.svg"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login page is missing %q", want)
		}
	}

	r := httptest.NewRequest("POST", "/portal-admin/login",
		strings.NewReader(url.Values{"username": {"admin"}, "password": {"secret-password"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/portal-admin/" {
		t.Fatalf("login: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Path != "/portal-admin" {
		t.Fatalf("cookies %+v", cookies)
	}

	r = httptest.NewRequest("GET", "/portal-admin/channels", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body = w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `href="/portal-admin/channels/new"`) || strings.Contains(body, `href="/channels`) {
		t.Errorf("channels page: %d", w.Code)
	}
}

func TestRootPathUnchanged(t *testing.T) {
	h, _ := newTestHandler(t, "")
	w := do(h, "GET", "/login", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `href="/static/style.css"`) ||
		!strings.Contains(w.Body.String(), `action="/login"`) {
		t.Errorf("login page at the root: %d", w.Code)
	}
	if w := do(h, "GET", "/", "", ""); w.Header().Get("Location") != "/login" {
		t.Errorf("redirect at the root: %q", w.Header().Get("Location"))
	}
}
