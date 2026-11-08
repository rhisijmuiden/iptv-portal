package portal

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func login(t *testing.T, f fixture, username, password string) *browser {
	t.Helper()
	b := newBrowser(t, f)
	if code, _, _ := b.do("POST", "/account/login", url.Values{"username": {username}, "password": {password}}); code != http.StatusSeeOther {
		t.Fatalf("login %s: %d", username, code)
	}
	return b
}

func TestAccountGuides(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := login(t, f, "jan", "secret1")
	_, _, body := b.do("GET", "/account/setup", nil)
	if !strings.Contains(body, "Formuler with MyTVOnline") || !strings.Contains(body, "Step-by-step guides") {
		t.Fatalf("setup page lacks the guides:\n%s", body)
	}
	list, _ := f.store.Guides(ctx, false)
	if len(list) < 5 {
		t.Fatalf("%d default guides", len(list))
	}
	code, _, body := b.do("GET", "/account/guides/"+strconv.FormatInt(list[0].ID, 10), nil)
	if code != http.StatusOK || !strings.Contains(body, "<code>"+f.srv.URL+"</code>") || !strings.Contains(body, "<code>jan</code>") {
		t.Fatalf("guide without the user's details: %d\n%s", code, body)
	}
	// A hidden guide is not shown.
	g := list[0]
	g.Enabled = false
	if _, err := f.store.SaveGuide(ctx, g); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b.do("GET", "/account/guides/"+strconv.FormatInt(g.ID, 10), nil); code != http.StatusNotFound {
		t.Errorf("hidden guide: %d", code)
	}
}

func TestAccountDevices(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := login(t, f, "jan", "secret1")
	csrf := b.csrf("/account/devices")
	if code, _, body := b.do("POST", "/account/devices", url.Values{"csrf": {csrf}, "name": {"Living room"}, "mac": {"00-1a-79-12-34-zz"}}); code != http.StatusBadRequest ||
		!strings.Contains(body, "Enter a MAC address") {
		t.Fatalf("bad MAC: %d\n%s", code, body)
	}
	if code, loc, _ := b.do("POST", "/account/devices", url.Values{"csrf": {csrf}, "name": {"Living room"}, "mac": {"001a79123456"}}); code != http.StatusSeeOther ||
		!strings.HasPrefix(loc, "/account/devices") {
		t.Fatalf("add: %d %s", code, loc)
	}
	_, _, body := b.do("GET", "/account/devices", nil)
	if !strings.Contains(body, "00:1A:79:12:34:56") || !strings.Contains(body, "Living room") {
		t.Fatalf("device not listed:\n%s", body)
	}

	// Watching shows up as a used device.
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	now := time.Now()
	f.store.StartView(ctx, store.View{UserID: u.ID, Username: "jan", ChannelID: f.nh, Channel: "NH", Kind: store.KindTV,
		IP: "192.0.2.7", UserAgent: "MyTVOnline 3", StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-30 * time.Minute)})
	f.act.Play(u.ID, "jan", "192.0.2.7", "MyTVOnline 3", f.nh, "NH")
	_, _, body = b.do("GET", "/account/devices", nil)
	if !strings.Contains(body, "Formuler / MyTVOnline") || !strings.Contains(body, "192.0.2.7") || !strings.Contains(body, "Watching now") {
		t.Fatalf("used device not shown:\n%s", body)
	}

	// Another user cannot remove it.
	pkgs, _ := f.store.Packages(ctx)
	if _, err := f.store.SaveUser(ctx, store.User{Username: "piet", Enabled: true, PackageIDs: []int64{pkgs[0].ID}}, "secret2"); err != nil {
		t.Fatal(err)
	}
	devs, _ := f.store.UserDevices(ctx, u.ID)
	other := login(t, f, "piet", "secret2")
	path := "/account/devices/" + strconv.FormatInt(devs[0].ID, 10) + "/delete"
	if code, _, _ := other.do("POST", path, url.Values{"csrf": {other.csrf("/account/devices")}}); code != http.StatusNotFound {
		t.Errorf("other user's device: %d", code)
	}
	if code, _, _ := b.do("POST", path, url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Errorf("remove: %d", code)
	}
	if devs, _ := f.store.UserDevices(ctx, u.ID); len(devs) != 0 {
		t.Errorf("device not removed: %v", devs)
	}
}

func TestAccountPackageRequest(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cats, _ := f.store.Categories(ctx)
	var east int64
	for _, c := range cats {
		if c.Name == "East" {
			east = c.ID
		}
	}
	plus, err := f.store.SavePackage(ctx, store.Package{Name: "Plus", CategoryIDs: []int64{east}, Public: true,
		Description: "Also the east", Price: "€ 3 per month"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, _ := f.store.SavePackage(ctx, store.Package{Name: "Secret", AllChannels: true})

	b := login(t, f, "jan", "secret1")
	_, _, body := b.do("GET", "/account/packages", nil)
	for _, want := range []string{"Plus", "€ 3 per month", "Also the east", "Your package", "1 channel you do not have yet", "Gld"} {
		if !strings.Contains(body, want) {
			t.Errorf("packages page lacks %q", want)
		}
	}
	if strings.Contains(body, "Secret") {
		t.Error("a package that is not public is shown")
	}
	csrf := b.csrf("/account/packages")
	if code, _, _ := b.do("POST", "/account/packages/"+strconv.FormatInt(hidden, 10)+"/request", url.Values{"csrf": {csrf}}); code != http.StatusBadRequest {
		t.Errorf("hidden package request: %d", code)
	}
	if code, _, _ := b.do("POST", "/account/packages/"+strconv.FormatInt(plus, 10)+"/request", url.Values{"csrf": {csrf}, "message": {"please"}}); code != http.StatusSeeOther {
		t.Fatalf("request: %d", code)
	}
	if code, _, body := b.do("POST", "/account/packages/"+strconv.FormatInt(plus, 10)+"/request", url.Values{"csrf": {csrf}}); code != http.StatusBadRequest ||
		!strings.Contains(body, "You already asked for this package.") {
		t.Errorf("second request: %d", code)
	}
	if n, _ := f.store.CountPendingRequests(ctx); n != 1 {
		t.Fatalf("%d pending requests", n)
	}
	open, _, _ := f.store.PackageRequests(ctx, 10)
	if _, err := f.store.DecidePackageRequest(ctx, open[0].ID, true, false, "enjoy"); err != nil {
		t.Fatal(err)
	}
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	if len(u.PackageIDs) != 2 {
		t.Errorf("packages after approval: %v", u.PackageIDs)
	}
	_, _, body = b.do("GET", "/account/packages", nil)
	if !strings.Contains(body, "Approved") || !strings.Contains(body, "enjoy") {
		t.Errorf("answer not shown:\n%s", body)
	}
}

func TestAccountHistoryKinds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	now := time.Now()
	f.store.StartView(ctx, store.View{UserID: u.ID, Username: "jan", ChannelID: f.nh, Channel: "NH", Kind: store.KindTV,
		StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-30 * time.Minute)})
	f.store.StartView(ctx, store.View{UserID: u.ID, Username: "jan", Channel: "Film", Kind: store.KindVOD,
		StartedAt: now.Add(-3 * time.Hour), EndedAt: now.Add(-90 * time.Minute)})
	b := login(t, f, "jan", "secret1")
	_, _, body := b.do("GET", "/account/history", nil)
	for _, want := range []string{"When you watch", "What you watch", "75%", "25%"} {
		if !strings.Contains(body, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	_, _, body = b.do("GET", "/account/history?kind=vod", nil)
	if strings.Contains(body, ">NH<") || !strings.Contains(body, "Film") {
		t.Errorf("kind filter does not filter:\n%s", body)
	}
}
