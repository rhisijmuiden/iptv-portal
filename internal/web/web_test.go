package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardedHeaders(t *testing.T) {
	req := func(remote string) *http.Request {
		r := httptest.NewRequest("GET", "http://portal.local/", nil)
		r.RemoteAddr = remote + ":5000"
		r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.5")
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "tv.example.com")
		return r
	}
	t.Cleanup(func() { SetTrustedProxies(nil) })

	// Without trusted proxies: today's rules.
	SetTrustedProxies(nil)
	if ip := ClientIP(req("10.0.0.1")); ip != "10.0.0.5" {
		t.Errorf("private proxy: ClientIP = %s", ip)
	}
	if ip := ClientIP(req("198.51.100.1")); ip != "198.51.100.1" {
		t.Errorf("public client: ClientIP = %s", ip)
	}
	if !IsHTTPS(req("198.51.100.1")) {
		t.Error("X-Forwarded-Proto should be read without trusted proxies")
	}
	if u := BaseURL("", req("10.0.0.1")); u != "https://portal.local" {
		t.Errorf("BaseURL = %s", u)
	}

	// With trusted proxies: only from those.
	_, n, _ := net.ParseCIDR("10.0.0.0/24")
	SetTrustedProxies([]*net.IPNet{n})
	if ip := ClientIP(req("10.0.0.1")); ip != "203.0.113.9" {
		t.Errorf("trusted proxy: ClientIP = %s", ip)
	}
	if ip := ClientIP(req("192.168.1.1")); ip != "192.168.1.1" {
		t.Errorf("untrusted private address: ClientIP = %s", ip)
	}
	if IsHTTPS(req("192.168.1.1")) {
		t.Error("X-Forwarded-Proto believed from an untrusted address")
	}
	if u := BaseURL("", req("10.0.0.1")); u != "https://tv.example.com" {
		t.Errorf("trusted BaseURL = %s", u)
	}
	if u := BaseURL("", req("192.168.1.1")); u != "http://portal.local" {
		t.Errorf("untrusted BaseURL = %s", u)
	}
}
