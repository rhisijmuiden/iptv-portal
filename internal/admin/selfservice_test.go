package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestAdminGuides(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	body := req("GET", "/guides", nil).Body.String()
	if !strings.Contains(body, "Formuler with MyTVOnline") || !strings.Contains(body, "Standard") {
		t.Fatalf("guide list:\n%s", body)
	}
	w := req("POST", "/guides/new", url.Values{"title": {"Our box"}, "body": {"1. Plug it in\n2. Enter `{server}`"}, "icon": {"box"}, "enabled": {"on"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", w.Code, w.Body)
	}
	list, _ := st.Guides(context.Background(), false)
	var id, builtin int64
	for _, g := range list {
		if g.Title == "Our box" {
			id = g.ID
		} else if g.Builtin != "" {
			builtin = g.ID
		}
	}
	body = req("GET", "/guides/"+itoa(id), nil).Body.String()
	if !strings.Contains(body, "<li>Plug it in</li>") || !strings.Contains(body, "Preview") {
		t.Errorf("edit page without preview:\n%s", body)
	}
	if w := req("POST", "/guides/new", url.Values{"title": {"Empty"}}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Text must not be empty.") {
		t.Errorf("empty text: %d", w.Code)
	}
	req("POST", "/guides/"+itoa(builtin)+"/delete", url.Values{})
	if body := req("GET", "/guides", nil).Body.String(); !strings.Contains(body, "Add 1 standard guide back") {
		t.Error("no restore button after deleting a standard guide")
	}
	if w := req("POST", "/guides/restore", url.Values{}); !strings.Contains(w.Header().Get("Location"), "ok=1+standard+guide") {
		t.Errorf("restore: %q", w.Header().Get("Location"))
	}
}

func TestAdminPackageRequests(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	basic, _ := st.SavePackage(ctx, store.Package{Name: "Basic", AllChannels: true, Public: true})
	plus, _ := st.SavePackage(ctx, store.Package{Name: "Plus", AllChannels: true, OnDemand: true, Public: true})
	uid, _ := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true, PackageIDs: []int64{basic}}, "secret1")
	rq, err := st.RequestPackage(ctx, uid, plus, "for the films")
	if err != nil {
		t.Fatal(err)
	}
	body := req("GET", "/requests", nil).Body.String()
	for _, want := range []string{"for the films", "Approve, replace their packages", `<span class="nav-count"`} {
		if !strings.Contains(body, want) {
			t.Errorf("requests page lacks %q", want)
		}
	}
	w := req("POST", "/requests/"+itoa(rq.ID), url.Values{"action": {"replace"}, "reply": {"done"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "ok=") {
		t.Fatalf("approve: %d %q", w.Code, w.Header().Get("Location"))
	}
	u, _ := st.User(ctx, uid)
	if len(u.PackageIDs) != 1 || u.PackageIDs[0] != plus {
		t.Errorf("packages: %v", u.PackageIDs)
	}
	if w := req("POST", "/requests/"+itoa(rq.ID), url.Values{"action": {"reject"}}); !strings.Contains(w.Header().Get("Location"), "err=") {
		t.Errorf("second answer: %q", w.Header().Get("Location"))
	}

	// The package form keeps the account page fields.
	w = req("POST", "/packages/"+itoa(plus), url.Values{"name": {"Plus"}, "all_channels": {"on"}, "description": {"Films too"}, "price": {"€ 8"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save package: %d", w.Code)
	}
	p, _ := st.Package(ctx, plus)
	if p.Description != "Films too" || p.Price != "€ 8" || p.Public {
		t.Errorf("package after save: %+v", p)
	}
}

func TestAdminUserDevices(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	uid, _ := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true}, "secret1")
	d, err := st.AddUserDevice(ctx, uid, "Living room", "00:1A:79:12:34:56")
	if err != nil {
		t.Fatal(err)
	}
	if body := req("GET", "/users/"+itoa(uid), nil).Body.String(); !strings.Contains(body, "00:1A:79:12:34:56") {
		t.Error("user page lacks the device")
	}
	if w := req("POST", "/users/"+itoa(uid)+"/devices/"+itoa(d.ID)+"/delete", url.Values{}); w.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", w.Code)
	}
	if devs, _ := st.UserDevices(ctx, uid); len(devs) != 0 {
		t.Error("device not removed")
	}
}
