package admin

import (
	"io"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestConnectionLimitForms(t *testing.T) {
	h := &Handler{}
	if err := h.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	render := func(name string, data any) string {
		var b strings.Builder
		err := h.pages[name].ExecuteTemplate(io.Writer(&b), "layout.html",
			page{Title: name, Session: &store.Session{}, Data: data})
		if err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	out := render("user", userFormData{User: store.User{ID: 4, Username: "jan", PackageConnections: 2}, Connections: 1})
	for _, want := range []string{`name="max_connections" value=""`, `placeholder="2 (from packages)"`,
		"Watching from <strong>1</strong> place now, 2 allowed.", `action="/users/4/release"`} {
		if !strings.Contains(out, want) {
			t.Errorf("user page is missing %q", want)
		}
	}
	out = render("user", userFormData{User: store.User{ID: 4, Username: "jan", MaxConnections: 3}})
	for _, want := range []string{`value="3"`, "Watching from <strong>0</strong> places now, 3 allowed.", " disabled>Release"} {
		if !strings.Contains(out, want) {
			t.Errorf("user page with override is missing %q", want)
		}
	}

	out = render("package", packageFormData{Package: store.Package{Name: "Family", MaxConnections: 3}})
	if !strings.Contains(out, `name="max_connections" value="3"`) {
		t.Error("package page is missing the connection limit")
	}
	out = render("packages", packageListData{Packages: []store.Package{{Name: "A", AllChannels: true}, {Name: "B", AllChannels: true, MaxConnections: 2}}})
	if !strings.Contains(out, "no limit") || !strings.Contains(out, "<td>2</td>") {
		t.Error("package list is missing the connection limits")
	}
}
