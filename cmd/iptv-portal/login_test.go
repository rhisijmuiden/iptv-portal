package main

import (
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestLoginBanner(t *testing.T) {
	settings := store.Settings{AdminPort: 8080, BasePath: "/iptv"}
	got := loginBanner(settings, &store.InitialCredentials{Username: `ad\min`, Password: "abc123"})
	for _, want := range []string{`http://\4:8080/iptv/`, `Username:           ad\\min`, "One-time password:  abc123"} {
		if !strings.Contains(got, want) {
			t.Errorf("banner misses %q:\n%s", want, got)
		}
	}
	if got := loginBanner(settings, nil); strings.Contains(got, "password") {
		t.Errorf("banner without login shows a password line:\n%s", got)
	}
}
