package admin

import (
	"io"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/update"
)

func TestInfoReleaseChannel(t *testing.T) {
	h := &Handler{}
	if err := h.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	render := func(d infoData) string {
		var b strings.Builder
		err := h.pages["info"].ExecuteTemplate(io.Writer(&b), "layout.html",
			page{Title: "Info & updates", Session: &store.Session{}, Data: d})
		if err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	out := render(infoData{Version: "v1.0.0", Status: update.Status{Current: "v1.0.0", Channel: update.Test,
		Releases: []update.Release{{Tag: "v1.1.0-rc.1", Prerelease: true}, {Tag: "v1.0.0"}}}})
	for _, want := range []string{`<option value="test" selected>`, `<span class="badge warn">test</span>`,
		`Update to v1.1.0-rc.1`, `<span class="badge warn">pre-release</span>`} {
		if !strings.Contains(out, want) {
			t.Errorf("test channel page is missing %q", want)
		}
	}

	out = render(infoData{Version: "v1.1.0-rc.1", Prerelease: true,
		Status: update.Status{Current: "v1.1.0-rc.1", Channel: update.Stable}})
	for _, want := range []string{`<option value="stable" selected>`, "stable releases only",
		"running a pre-release on the stable channel"} {
		if !strings.Contains(out, want) {
			t.Errorf("stable channel page is missing %q", want)
		}
	}
}
