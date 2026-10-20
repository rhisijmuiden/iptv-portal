// Package entratest runs a fake Microsoft Entra tenant for tests: OpenID
// discovery, signing keys, an authorize endpoint that signs the user in
// right away, and a token endpoint that checks the code, PKCE verifier and
// client secret.
package entratest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Tenant, ClientID and Secret are what the fake tenant accepts.
const (
	Tenant   = "11111111-2222-3333-4444-555555555555"
	ClientID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	Secret   = "fake-client-secret"
)

type grant struct {
	nonce, challenge, redirect string
	claims                     map[string]any
}

// Server is a fake Entra tenant.
type Server struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu sync.Mutex
	// Claims are put in the next ID token, next to iss, aud, tid, nonce
	// and the times. Change them before signing in.
	Claims map[string]any
	codes  map[string]grant
}

// New starts a fake tenant; it is closed when the test ends.
func New(t interface {
	Helper()
	Cleanup(func())
	Fatal(...any)
}) *Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{key: key, codes: map[string]grant{}, Claims: map[string]any{
		"preferred_username": "Jane@Example.com", "name": "Jane Doe", "oid": "99999999-0000-0000-0000-000000000001",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /"+Tenant+"/v2.0/.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /"+Tenant+"/discovery/v2.0/keys", s.keys)
	mux.HandleFunc("GET /"+Tenant+"/oauth2/v2.0/authorize", s.authorize)
	mux.HandleFunc("POST /"+Tenant+"/oauth2/v2.0/token", s.token)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// Issuer is the issuer of the tokens.
func (s *Server) Issuer() string { return s.URL + "/" + Tenant + "/v2.0" }

func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	base := s.URL + "/" + Tenant
	json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                s.Issuer(),
		"authorization_endpoint":                base + "/oauth2/v2.0/authorize",
		"token_endpoint":                        base + "/oauth2/v2.0/token",
		"jwks_uri":                              base + "/discovery/v2.0/keys",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &s.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"},
	}})
}

// authorize signs the user in at once and sends the browser back.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	b := make([]byte, 16)
	rand.Read(b)
	code := hex.EncodeToString(b)
	s.mu.Lock()
	claims := map[string]any{}
	for k, v := range s.Claims {
		claims[k] = v
	}
	s.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), claims: claims}
	s.mu.Unlock()
	back, _ := url.Parse(q.Get("redirect_uri"))
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	fail := func(desc string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": desc + "\r\nTrace ID: x"})
	}
	r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != ClientID || secret != Secret {
		fail("AADSTS7000215: Invalid client secret provided.")
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	g, found := s.codes[code]
	delete(s.codes, code)
	s.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case !found:
		fail("AADSTS70008: The provided authorization code has expired.")
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		fail("AADSTS501481: The Code_Verifier does not match the code_challenge.")
		return
	case r.PostForm.Get("redirect_uri") != g.redirect:
		fail("AADSTS50011: The redirect URI does not match.")
		return
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: s.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now()
	claims := map[string]any{"iss": s.Issuer(), "aud": ClientID, "tid": Tenant, "nonce": g.nonce,
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix(), "sub": "subject"}
	for k, v := range g.claims {
		claims[k] = v
	}
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
}
