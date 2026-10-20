package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/entra"
	"github.com/rhisijmuiden/iptv-portal/internal/entra/entratest"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// newSSOHandler sets up the panel with sign-in through a fake tenant; nil
// groups lets every account in.
func newSSOHandler(t *testing.T, base string, groups []string) (*Handler, *store.Store, *entratest.Server) {
	t.Helper()
	idp := entratest.New(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	err = st.SetSSO(context.Background(), entra.Config{Enabled: true, TenantID: entratest.Tenant,
		ClientID: entratest.ClientID, ClientSecret: entratest.Secret, AllowedGroups: groups, AllowAll: groups == nil})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Store: st, BasePath: base, Entra: &entra.Client{Authority: idp.URL}, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return h, st, idp
}

// ssoSignIn runs a Microsoft sign-in through the panel and the fake tenant
// and returns the response to the callback.
func ssoSignIn(t *testing.T, h http.Handler, base string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", base+"/login/entra", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	var state *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == stateCookie {
			state = c
		}
	}
	if state == nil {
		t.Fatal("no state cookie")
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d %v", resp.StatusCode, err)
	}
	if want := "http://example.com" + base + entra.CallbackPath; back.Scheme+"://"+back.Host+back.Path != want {
		t.Fatalf("redirect URI %s, want %s", back, want)
	}
	r = httptest.NewRequest("GET", back.RequestURI(), nil)
	r.AddCookie(state)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionFrom(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

func TestSSOSignIn(t *testing.T) {
	for _, base := range []string{"", "/portal-admin"} {
		h, st, _ := newSSOHandler(t, base, []string{"admins-group", "Portal.Admin"})
		w := do(h, "GET", base+"/login", "", "")
		if !strings.Contains(w.Body.String(), "Sign in with Microsoft") {
			t.Fatalf("base %q: login page has no Microsoft button", base)
		}
		// No allowed group or role: refused, and no account is created.
		w = ssoSignIn(t, h, base)
		if w.Code != http.StatusForbidden || sessionFrom(w) != nil {
			t.Fatalf("base %q without group: %d %s", base, w.Code, w.Body)
		}
		if n, _ := st.AdminCount(context.Background()); n != 0 {
			t.Fatalf("base %q: %d admins after a refused sign-in", base, n)
		}
	}
}

func TestSSOSignInWithRole(t *testing.T) {
	h, st, idp := newSSOHandler(t, "/portal-admin", []string{"admins-group", "Portal.Admin"})
	idp.Claims["roles"] = []string{"Portal.Admin"}
	w := ssoSignIn(t, h, "/portal-admin")
	sess := sessionFrom(w)
	if w.Code != http.StatusSeeOther || sess == nil || w.Header().Get("Location") != "/portal-admin/" {
		t.Fatalf("sign-in: %d %v %s", w.Code, w.Header(), w.Body)
	}
	r := httptest.NewRequest("GET", "/portal-admin/settings", nil)
	r.AddCookie(sess)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), "jane@example.com") {
		t.Fatalf("settings after sign-in: %d", rw.Code)
	}
	// A second sign-in reuses the account.
	idp.Claims["groups"] = []string{"admins-group"}
	delete(idp.Claims, "roles")
	if w := ssoSignIn(t, h, "/portal-admin"); sessionFrom(w) == nil {
		t.Fatalf("second sign-in: %d %s", w.Code, w.Body)
	}
	if n, _ := st.AdminCount(context.Background()); n != 1 {
		t.Fatalf("%d admins, want 1", n)
	}
	// The random password of the account is not usable.
	if _, err := st.Authenticate(context.Background(), "jane@example.com", ""); err == nil {
		t.Fatal("empty password accepted")
	}
}

func TestSSOExistingLocalAdmin(t *testing.T) {
	h, st, idp := newSSOHandler(t, "", nil)
	ctx := context.Background()
	if _, err := st.CreateAdmin(ctx, "jane@example.com", "local-password", false); err != nil {
		t.Fatal(err)
	}
	idp.Claims["preferred_username"] = "JANE@example.com"
	if w := ssoSignIn(t, h, ""); sessionFrom(w) == nil {
		t.Fatalf("sign-in: %d %s", w.Code, w.Body)
	}
	if _, err := st.Authenticate(ctx, "jane@example.com", "local-password"); err != nil {
		t.Fatalf("local password no longer works: %v", err)
	}
}

func TestSSOStateMismatch(t *testing.T) {
	h, _, _ := newSSOHandler(t, "", nil)
	r := httptest.NewRequest("GET", entra.CallbackPath+"?state=abc&code=def", nil)
	r.AddCookie(&http.Cookie{Name: stateCookie, Value: "other"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || sessionFrom(w) != nil {
		t.Fatalf("mismatched state: %d", w.Code)
	}
	w = do(h, "GET", entra.CallbackPath+"?error=access_denied&error_description=AADSTS65004%3A+User+declined", "", "")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "AADSTS65004") {
		t.Fatalf("refused sign-in: %d %s", w.Code, w.Body)
	}
}

func TestSSODisabled(t *testing.T) {
	h, _ := newTestHandler(t, "")
	if w := do(h, "GET", "/login", "", ""); strings.Contains(w.Body.String(), "Sign in with Microsoft") {
		t.Fatal("Microsoft button shown while SSO is off")
	}
	if w := do(h, "GET", "/login/entra", "", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("start while off: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestSSOSettings(t *testing.T) {
	h, st := newTestHandler(t, "")
	ctx := context.Background()
	a, err := st.CreateAdmin(ctx, "admin", "password123", false)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	post := func(form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf", sess.CSRF)
		r := httptest.NewRequest("POST", "/settings/sso", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	form := url.Values{"sso_enabled": {"on"}, "sso_tenant_id": {entratest.Tenant}, "sso_client_id": {entratest.ClientID},
		"sso_client_secret": {"secret"}, "sso_allowed_groups": {"g1\r\ng2"}, "sso_admin_url": {"https://admin.example.com/"}}
	if w := post(form); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	// An empty secret keeps the stored one.
	form.Set("sso_client_secret", "")
	if w := post(form); w.Code != http.StatusSeeOther {
		t.Fatalf("save without secret: %d %s", w.Code, w.Body)
	}
	s, _ := st.Settings(ctx)
	if s.SSO.ClientSecret != "secret" || strings.Join(s.SSO.AllowedGroups, ",") != "g1,g2" || !s.SSO.Enabled {
		t.Fatalf("stored %+v", s.SSO)
	}
	// Nobody allowed: refused, and the form shows the error.
	form.Del("sso_allowed_groups")
	if w := post(form); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "at least one allowed group") {
		t.Fatalf("save without groups: %d", w.Code)
	}

	r := httptest.NewRequest("GET", "/settings", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.Token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "https://admin.example.com/login/entra/callback") || strings.Contains(body, `value="secret"`) {
		t.Fatalf("settings page: %d", w.Code)
	}
}
