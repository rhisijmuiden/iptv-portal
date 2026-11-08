package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestAdminUserParental(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	if _, err := st.ImportChannels(ctx, []store.ImportEntry{
		{Name: "NH", StreamURL: "https://s.example/nh.m3u8", Group: "News"},
		{Name: "Kids1", StreamURL: "https://s.example/k.m3u8", Group: "Kids"},
	}); err != nil {
		t.Fatal(err)
	}
	pkg, _ := st.SavePackage(ctx, store.Package{Name: "All", AllChannels: true})
	uid, _ := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true, PackageIDs: []int64{pkg}}, "secret1")
	chs, _ := st.Channels(ctx, store.ChannelFilter{})
	var kids int64
	for _, c := range chs {
		if c.Category == "Kids" {
			kids = c.CategoryID
			st.SetFavourite(ctx, uid, c.ID, true)
		}
	}
	st.SetParentalPIN(ctx, uid, "1234")
	until := time.Now().Add(time.Hour)
	st.SetUnlockedUntil(ctx, uid, &until)

	body := req("GET", "/users/"+itoa(uid), nil).Body.String()
	for _, want := range []string{`id="parental"`, "Kids1", "PIN set", "Unlocked until", "Nothing is locked yet.", "Remove PIN"} {
		if !strings.Contains(body, want) {
			t.Errorf("user page lacks %q", want)
		}
	}
	w := req("POST", "/users/"+itoa(uid)+"/parental", url.Values{"group": {itoa(kids)}, "max_age": {"12"}})
	if w.Code != http.StatusSeeOther || !strings.HasSuffix(w.Header().Get("Location"), "#parental") {
		t.Fatalf("save: %d %s", w.Code, w.Header().Get("Location"))
	}
	req("POST", "/users/"+itoa(uid)+"/parental/lock", url.Values{})
	req("POST", "/users/"+itoa(uid)+"/parental/pin/delete", url.Values{})
	p, _ := st.Preferences(ctx, uid)
	if !p.Categories[kids].Locked || p.Parental.MaxAge != 12 || p.Parental.PINSet || p.Parental.Unlocked(time.Now()) {
		t.Errorf("preferences = %+v", p)
	}
	if body := req("GET", "/users/"+itoa(uid), nil).Body.String(); !strings.Contains(body, "1 channel group locked") || !strings.Contains(body, "No PIN") {
		t.Errorf("user page after saving:\n%s", body)
	}
	if w := req("POST", "/users/999/parental", url.Values{}); w.Code != http.StatusNotFound {
		t.Errorf("unknown user: %d", w.Code)
	}
}
