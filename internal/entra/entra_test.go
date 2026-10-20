package entra_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/entra"
	"github.com/rhisijmuiden/iptv-portal/internal/entra/entratest"
)

func TestValidate(t *testing.T) {
	ok := entra.Config{Enabled: true, TenantID: " " + strings.ToUpper(entratest.Tenant), ClientID: entratest.ClientID,
		ClientSecret: "s", AdminURL: "https://admin.example.com/", AllowedGroups: []string{"a, b\nc", "a"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.TenantID != entratest.Tenant || ok.AdminURL != "https://admin.example.com" || strings.Join(ok.AllowedGroups, "|") != "a|b|c" {
		t.Errorf("not normalised: %+v", ok)
	}
	for name, c := range map[string]entra.Config{
		"domain tenant": {Enabled: true, TenantID: "contoso.onmicrosoft.com", ClientID: entratest.ClientID, ClientSecret: "s", AllowAll: true},
		"common tenant": {Enabled: true, TenantID: "common", ClientID: entratest.ClientID, ClientSecret: "s", AllowAll: true},
		"no secret":     {Enabled: true, TenantID: entratest.Tenant, ClientID: entratest.ClientID, AllowAll: true},
		"nobody":        {Enabled: true, TenantID: entratest.Tenant, ClientID: entratest.ClientID, ClientSecret: "s"},
		"bad URL":       {AdminURL: "admin.example.com"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&entra.Config{TenantID: "half done"}).Validate(); err != nil {
		t.Errorf("disabled config rejected: %v", err)
	}
}

// signIn starts a sign-in, lets the fake tenant answer and returns the
// state and code from the redirect.
func signIn(t *testing.T, c *entra.Client, cfg entra.Config) (state, code string) {
	t.Helper()
	authURL, state, err := c.Begin(context.Background(), cfg, "https://admin.example.com/login/entra/callback")
	if err != nil {
		t.Fatal(err)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	if back.Query().Get("state") != state {
		t.Fatalf("state %q came back as %q", state, back.Query().Get("state"))
	}
	return state, back.Query().Get("code")
}

func setup(t *testing.T) (*entratest.Server, *entra.Client, entra.Config) {
	idp := entratest.New(t)
	cfg := entra.Config{Enabled: true, TenantID: entratest.Tenant, ClientID: entratest.ClientID,
		ClientSecret: entratest.Secret, AllowedGroups: []string{"g1"}}
	return idp, &entra.Client{Authority: idp.URL}, cfg
}

func TestFinish(t *testing.T) {
	idp, c, cfg := setup(t)
	idp.Claims["groups"] = []string{"g0", "G1"}
	state, code := signIn(t, c, cfg)
	id, err := c.Finish(context.Background(), state, code)
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jane@example.com" || id.Name != "Jane Doe" || id.TenantID != entratest.Tenant {
		t.Errorf("identity %+v", id)
	}
	// A state can only be used once.
	if _, err := c.Finish(context.Background(), state, code); err == nil {
		t.Error("state reused")
	}
}

func TestFinishRefused(t *testing.T) {
	idp, c, cfg := setup(t)
	state, code := signIn(t, c, cfg)
	if _, err := c.Finish(context.Background(), state, code); !errors.Is(err, entra.ErrNotAllowed) {
		t.Errorf("without group: %v", err)
	}

	// Entra leaves the groups out and points to Graph when there are many.
	idp.Claims["_claim_names"] = map[string]string{"groups": "src1"}
	idp.Claims["_claim_sources"] = map[string]any{"src1": map[string]string{"endpoint": "https://graph.microsoft.com/v1.0/users/x/getMemberObjects"}}
	state, code = signIn(t, c, cfg)
	if _, err := c.Finish(context.Background(), state, code); err == nil || !strings.Contains(err.Error(), "too many groups") {
		t.Errorf("group overage: %v", err)
	}

	cfg.ClientSecret = "wrong"
	state, code = signIn(t, c, cfg)
	_, err := c.Finish(context.Background(), state, code)
	if err == nil || !strings.Contains(err.Error(), "AADSTS7000215") || strings.Contains(err.Error(), "Trace ID") {
		t.Errorf("wrong secret: %v", err)
	}

	// A token for another tenant is refused even when it is signed right.
	cfg.ClientSecret = entratest.Secret
	cfg.AllowAll = true
	delete(idp.Claims, "_claim_names")
	delete(idp.Claims, "_claim_sources")
	idp.Claims["tid"] = "00000000-0000-0000-0000-000000000000"
	state, code = signIn(t, c, cfg)
	if _, err := c.Finish(context.Background(), state, code); err == nil || !strings.Contains(err.Error(), "another tenant") {
		t.Errorf("other tenant: %v", err)
	}
}
