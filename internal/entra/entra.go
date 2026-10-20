// Package entra signs admins in to the admin panel with Microsoft Entra ID
// (formerly Azure AD) using OpenID Connect: the authorization code flow
// with PKCE and a client secret.
package entra

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config is how the admin panel talks to Entra. It is stored with the
// other settings.
type Config struct {
	// Enabled shows "Sign in with Microsoft" on the login page.
	Enabled bool
	// TenantID is the Directory (tenant) ID of the organisation.
	TenantID string
	// ClientID is the Application (client) ID of the app registration.
	ClientID string
	// ClientSecret is a client secret value of the app registration.
	ClientSecret string
	// AdminURL is the address of the admin panel as the browser sees it,
	// e.g. "https://admin.tv.example.com". The redirect URI is built from
	// it. Empty means "derive from the incoming request".
	AdminURL string
	// AllowedGroups are group object IDs or app role values; an account
	// needs at least one of them to get in.
	AllowedGroups []string
	// AllowAll lets every account of the tenant that can sign in to the
	// app in, without checking groups or roles.
	AllowAll bool
}

// CallbackPath is the path of the redirect URI, below the admin panel.
const CallbackPath = "/login/entra/callback"

var guid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Validate checks and normalises the configuration. When sign-in is off
// the fields are only trimmed, so a half-finished setup can be saved.
func (c *Config) Validate() error {
	c.TenantID = strings.ToLower(strings.TrimSpace(c.TenantID))
	c.ClientID = strings.ToLower(strings.TrimSpace(c.ClientID))
	c.ClientSecret = strings.TrimSpace(c.ClientSecret)
	c.AdminURL = strings.TrimRight(strings.TrimSpace(c.AdminURL), "/")
	var groups []string
	for _, g := range c.AllowedGroups {
		for _, g := range strings.FieldsFunc(g, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
	}
	c.AllowedGroups = groups
	if c.AdminURL != "" {
		u, err := url.Parse(c.AdminURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("admin panel URL must start with http:// or https://")
		}
	}
	if !c.Enabled {
		return nil
	}
	if !guid.MatchString(c.TenantID) {
		return errors.New("tenant ID must be the Directory (tenant) ID, a GUID like 00000000-0000-0000-0000-000000000000")
	}
	if !guid.MatchString(c.ClientID) {
		return errors.New("client ID must be the Application (client) ID, a GUID like 00000000-0000-0000-0000-000000000000")
	}
	if c.ClientSecret == "" {
		return errors.New("client secret must not be empty")
	}
	if len(c.AllowedGroups) == 0 && !c.AllowAll {
		return errors.New("enter at least one allowed group or app role, or allow every account of the tenant")
	}
	return nil
}

// Identity is the signed-in Entra account.
type Identity struct {
	// Username is the sign-in name (UPN or email), lower case. It is the
	// admin account name in the portal.
	Username string
	Name     string
	ObjectID string
	TenantID string
	// Groups holds the group object IDs and app roles in the token.
	Groups []string
}

// ErrNotAllowed is returned for an account without an allowed group or role.
var ErrNotAllowed = errors.New("your Microsoft account is not allowed to use this admin panel")

// pendingTTL is how long a started sign-in can be finished.
const pendingTTL = 10 * time.Minute

// maxPending caps the started sign-ins kept in memory.
const maxPending = 1000

type pending struct {
	nonce, verifier, redirect string
	cfg                       Config
	expires                   time.Time
}

// Client runs sign-ins. It keeps the started ones in memory, so they do not
// survive a restart.
type Client struct {
	// Authority is the Entra login host; tests point it at a fake server.
	Authority string

	mu        sync.Mutex
	providers map[string]*oidc.Provider
	pending   map[string]pending
}

// NewClient returns a client for the public Entra cloud.
func NewClient() *Client {
	return &Client{Authority: "https://login.microsoftonline.com"}
}

func (c *Client) issuer(tenant string) string {
	return strings.TrimRight(c.Authority, "/") + "/" + tenant + "/v2.0"
}

// provider returns the cached OpenID configuration of the tenant.
func (c *Client) provider(ctx context.Context, tenant string) (*oidc.Provider, error) {
	c.mu.Lock()
	p := c.providers[tenant]
	c.mu.Unlock()
	if p != nil {
		return p, nil
	}
	// The provider keeps using this context to fetch signing keys later,
	// so it must not be the request context.
	p, err := oidc.NewProvider(context.WithoutCancel(ctx), c.issuer(tenant))
	if err != nil {
		return nil, fmt.Errorf("reading the Entra configuration of the tenant: %w", err)
	}
	c.mu.Lock()
	if c.providers == nil {
		c.providers = map[string]*oidc.Provider{}
	}
	c.providers[tenant] = p
	c.mu.Unlock()
	return p, nil
}

func oauthConfig(p *oidc.Provider, cfg Config, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  redirect,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

func randomString() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Begin starts a sign-in. It returns the Microsoft login address to send
// the browser to and the state that the callback must bring back.
func (c *Client) Begin(ctx context.Context, cfg Config, redirect string) (authURL, state string, err error) {
	if !cfg.Enabled {
		return "", "", errors.New("sign-in with Microsoft is not enabled")
	}
	p, err := c.provider(ctx, cfg.TenantID)
	if err != nil {
		return "", "", err
	}
	state, nonce, verifier := randomString(), randomString(), oauth2.GenerateVerifier()
	now := time.Now()
	c.mu.Lock()
	if c.pending == nil {
		c.pending = map[string]pending{}
	}
	for k, v := range c.pending {
		if now.After(v.expires) {
			delete(c.pending, k)
		}
	}
	if len(c.pending) >= maxPending {
		c.mu.Unlock()
		return "", "", errors.New("too many sign-ins in progress, try again in a few minutes")
	}
	c.pending[state] = pending{nonce: nonce, verifier: verifier, redirect: redirect, cfg: cfg, expires: now.Add(pendingTTL)}
	c.mu.Unlock()
	authURL = oauthConfig(p, cfg, redirect).AuthCodeURL(state,
		oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("response_mode", "query"),
		oauth2.SetAuthURLParam("prompt", "select_account"))
	return authURL, state, nil
}

// Finish completes a sign-in: it redeems the code, verifies the ID token
// and checks the allowed groups. The configuration is the one Begin got, so
// a settings change in between cannot mix two setups.
func (c *Client) Finish(ctx context.Context, state, code string) (Identity, error) {
	c.mu.Lock()
	pend, ok := c.pending[state]
	delete(c.pending, state)
	c.mu.Unlock()
	if !ok || time.Now().After(pend.expires) {
		return Identity{}, errors.New("the sign-in expired, try again")
	}
	cfg := pend.cfg
	p, err := c.provider(ctx, cfg.TenantID)
	if err != nil {
		return Identity{}, err
	}
	tok, err := oauthConfig(p, cfg, pend.redirect).Exchange(ctx, code, oauth2.VerifierOption(pend.verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("redeeming the sign-in code: %w", describe(err))
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, errors.New("Entra returned no ID token")
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("checking the ID token: %w", err)
	}
	if idt.Nonce != pend.nonce {
		return Identity{}, errors.New("the ID token does not belong to this sign-in")
	}
	var claims struct {
		PreferredUsername string            `json:"preferred_username"`
		Email             string            `json:"email"`
		UPN               string            `json:"upn"`
		Name              string            `json:"name"`
		OID               string            `json:"oid"`
		TID               string            `json:"tid"`
		Groups            []string          `json:"groups"`
		Roles             []string          `json:"roles"`
		ClaimNames        map[string]string `json:"_claim_names"`
	}
	if err := idt.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("reading the ID token: %w", err)
	}
	if !strings.EqualFold(claims.TID, cfg.TenantID) {
		return Identity{}, errors.New("the account belongs to another tenant")
	}
	id := Identity{Name: claims.Name, ObjectID: claims.OID, TenantID: claims.TID,
		Groups: append(claims.Groups, claims.Roles...)}
	for _, u := range []string{claims.PreferredUsername, claims.Email, claims.UPN} {
		if u = strings.ToLower(strings.TrimSpace(u)); u != "" {
			id.Username = u
			break
		}
	}
	if id.Username == "" {
		return Identity{}, errors.New("the ID token holds no user name; add the email or upn claim to the app registration")
	}
	if cfg.AllowAll {
		return id, nil
	}
	for _, g := range id.Groups {
		for _, a := range cfg.AllowedGroups {
			if strings.EqualFold(g, a) {
				return id, nil
			}
		}
	}
	if _, over := claims.ClaimNames["groups"]; over {
		return Identity{}, errors.New("your account is in too many groups to list in the token; allow an app role instead of a group")
	}
	return Identity{}, ErrNotAllowed
}

// describe shortens an OAuth error to what Entra said.
func describe(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.ErrorDescription != "" {
		// Entra puts the AADSTS code first and trace IDs on later lines.
		d, _, _ := strings.Cut(re.ErrorDescription, "\r\n")
		d, _, _ = strings.Cut(d, "\n")
		return errors.New(strings.TrimSpace(d))
	}
	return err
}
