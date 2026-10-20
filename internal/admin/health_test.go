package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// loggedIn returns a function doing requests as a logged-in admin.
func loggedIn(t *testing.T, h http.Handler, st *store.Store) func(method, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.Background()
	a, err := st.CreateAdmin(ctx, "admin", "secret-password", false)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	return func(method, path string, form url.Values) *httptest.ResponseRecorder {
		var r *http.Request
		if form != nil {
			form.Set("csrf", sess.CSRF)
			r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}

func TestStreamHealthPages(t *testing.T) {
	streams := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok.ts" {
			w.Write([]byte("data"))
			return
		}
		http.NotFound(w, r)
	}))
	defer streams.Close()
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	good, _ := st.SaveChannel(ctx, store.Channel{Name: "Good", StreamURL: streams.URL + "/ok.ts", Enabled: true})
	bad, _ := st.SaveChannel(ctx, store.Channel{Name: "Bad", StreamURL: streams.URL + "/gone.m3u8", Enabled: true})
	st.SaveChannel(ctx, store.Channel{Name: "New", StreamURL: streams.URL + "/new.m3u8", Enabled: true})

	w := req("POST", "/channels/"+itoa(bad)+"/check", url.Values{"back": {"/channels?q=a"}})
	loc := w.Header().Get("Location")
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/channels?") || !strings.Contains(loc, "err=Bad+does+not+play") ||
		!strings.Contains(loc, "q=a") {
		t.Fatalf("check bad: %d %q", w.Code, loc)
	}
	w = req("POST", "/channels/"+itoa(good)+"/check", url.Values{})
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "ok=Good+works") {
		t.Fatalf("check good: %q", loc)
	}

	body := req("GET", "/channels", nil).Body.String()
	for _, want := range []string{">working</span>", ">failing</span>", ">unchecked</span>", "HTTP 404 Not Found"} {
		if !strings.Contains(body, want) {
			t.Errorf("channels page is missing %q", want)
		}
	}
	body = req("GET", "/channels?health=failing", nil).Body.String()
	if !strings.Contains(body, ">Bad</a>") || strings.Contains(body, ">Good</a>") || strings.Contains(body, ">New</a>") {
		t.Error("failing filter shows the wrong channels")
	}

	w = req("POST", "/health", url.Values{"enabled": {"on"}, "interval": {"30"}, "auto_disable": {"on"}, "disable_after": {"4"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save settings: %d %s", w.Code, w.Body)
	}
	if hs, _ := st.HealthSettings(ctx); hs != (store.HealthSettings{IntervalMinutes: 30, DisableAfter: 4}) {
		t.Errorf("saved %+v", hs)
	}
	w = req("POST", "/health", url.Values{"enabled": {"on"}, "interval": {"1"}})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Check interval must be between") {
		t.Errorf("invalid interval: %d", w.Code)
	}
	body = req("GET", "/health", nil).Body.String()
	for _, want := range []string{"Failing streams", ">Bad</a>", "HTTP 404 Not Found", `name="interval" min="5" max="10080" value="30"`} {
		if !strings.Contains(body, want) {
			t.Errorf("health page is missing %q", want)
		}
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
