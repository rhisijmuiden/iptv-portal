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

func TestStatsPage(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	jan, _ := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true}, "password123")
	nh, _ := st.SaveChannel(ctx, store.Channel{Name: "NH", StreamURL: "https://example.com/nh.m3u8", Enabled: true})
	idle, _ := st.SaveChannel(ctx, store.Channel{Name: "Idle", StreamURL: "https://example.com/idle.m3u8", Enabled: true})
	start := time.Now().Add(-time.Hour)
	st.StartView(ctx, store.View{UserID: jan, Username: "jan", ChannelID: nh, Channel: "NH", Kind: store.KindTV,
		IP: "10.0.0.1", UserAgent: "MyTVOnline", StartedAt: start, EndedAt: start.Add(80 * time.Minute)})

	body := req("GET", "/stats", nil).Body.String()
	for _, want := range []string{"Viewing statistics", "1 h 20 min", `href="/stats?days=30&amp;channel=` + itoa(nh) + `">NH</a>`,
		`href="/stats?days=30&amp;user=` + itoa(jan) + `">jan</a>`, `>Idle</a>`, `class="bar"`, `class="active"`} {
		if !strings.Contains(body, want) {
			t.Errorf("statistics page is missing %q", want)
		}
	}
	body = req("GET", "/stats?days=7&kind=radio", nil).Body.String()
	if strings.Contains(body, ">NH</a>") || !strings.Contains(body, "No views recorded") {
		t.Error("radio filter shows TV views")
	}
	body = req("GET", "/stats?user="+itoa(jan), nil).Body.String()
	if !strings.Contains(body, "<h1>jan</h1>") || !strings.Contains(body, "10.0.0.1 · MyTVOnline") || strings.Contains(body, "Nobody watched") {
		t.Error("user statistics are wrong")
	}
	body = req("GET", "/stats?channel="+itoa(nh), nil).Body.String()
	if !strings.Contains(body, "<h1>NH</h1>") || !strings.Contains(body, ">jan</a>") {
		t.Error("channel statistics are wrong")
	}
	if w := req("GET", "/stats?user=999", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown user: %d", w.Code)
	}

	w := req("POST", "/channels/"+itoa(idle)+"/toggle", url.Values{"enabled": {"0"}, "back": {"/stats?days=30"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/stats?days=30" {
		t.Fatalf("disable: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c, _ := st.Channel(ctx, idle); c.Enabled {
		t.Fatal("channel still enabled")
	}

	w = req("POST", "/stats/settings", url.Values{"days": {"30"}, "back": {"/stats"}})
	if !strings.Contains(w.Header().Get("Location"), "ok=") {
		t.Fatalf("settings: %q", w.Header().Get("Location"))
	}
	if d, _ := st.ViewRetention(ctx); d != 30 {
		t.Fatalf("retention = %d", d)
	}
	w = req("POST", "/stats/settings", url.Values{"days": {"-3"}})
	if !strings.Contains(w.Header().Get("Location"), "err=") {
		t.Fatalf("bad settings: %q", w.Header().Get("Location"))
	}
	req("POST", "/stats/clear", url.Values{})
	if sum, _ := st.ViewSummary(ctx, store.ViewFilter{}); sum.Views != 0 {
		t.Fatal("statistics not cleared")
	}
}
