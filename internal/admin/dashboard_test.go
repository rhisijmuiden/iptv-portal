package admin

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestDashboardViewers(t *testing.T) {
	h := &Handler{}
	if err := h.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	snap := activity.Snapshot{Now: now, OnlineUsers: 1, Watching: 1, Connections: []activity.Connection{
		{UserID: 4, Username: "jan", IP: "10.0.0.1", UserAgent: "MyTVOnline", LastSeen: now.Add(-2 * time.Minute),
			Channel: "NH", ChannelID: 7, ChannelAt: now.Add(-3 * time.Minute)},
		{UserID: 5, Username: "piet", IP: "10.0.0.2", LastSeen: now.Add(-2 * time.Hour)},
	}}
	var b strings.Builder
	err := h.pages["en"]["dashboard"].ExecuteTemplate(io.Writer(&b), "layout.html",
		page{Title: "Dashboard", Session: &store.Session{}, Data: dashboardData{Activity: snap, Window: 15}})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"online now, 1 watching", `href="/users/4">jan`, "watching</span>",
		"NH <span class=\"muted\">opened 3 min ago", "idle</span>", "2 h ago", "10.0.0.1 · MyTVOnline"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}
}
