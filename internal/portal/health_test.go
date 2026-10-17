package portal

import (
	"net/http"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

func TestHealthz(t *testing.T) {
	f := setup(t)
	resp := f.get(t, "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got map[string]any
	f.json(t, "/healthz", &got)
	if got["status"] != "ok" || got["app"] != "iptv-portal" || got["version"] != version.Version || got["contract"] != 1.0 {
		t.Errorf("healthz = %v", got)
	}
}
