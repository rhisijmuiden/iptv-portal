package stats

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestRecorder(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	uid, err := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true}, "password123")
	if err != nil {
		t.Fatal(err)
	}
	nh, _ := st.SaveChannel(ctx, store.Channel{Name: "NH", StreamURL: "https://example.com/nh.m3u8", Enabled: true})
	gld, _ := st.SaveChannel(ctx, store.Channel{Name: "Gld", StreamURL: "https://example.com/gld.m3u8", Enabled: true})

	start := time.Date(2026, 10, 25, 20, 0, 0, 0, time.Local)
	now := start
	rec := New(st, slog.New(slog.DiscardHandler))
	rec.now = func() time.Time { return now }

	rec.Open(ctx, uid, "jan", "10.0.0.1", "MyTVOnline", nh, "NH", store.KindTV)
	now = now.Add(2 * time.Minute)
	rec.Open(ctx, uid, "jan", "10.0.0.1", "MyTVOnline", nh, "NH", store.KindTV) // reconnect: same view
	now = now.Add(88 * time.Minute)
	rec.Open(ctx, uid, "jan", "10.0.0.1", "MyTVOnline", gld, "Gld", store.KindTV) // zapped after 90 min
	now = now.Add(10 * time.Minute)
	rec.Seen(ctx, uid, "10.0.0.1") // guide request while Gld plays
	now = now.Add(14 * time.Minute)
	rec.Seen(ctx, uid, "10.0.0.1") // after the assumed end, but still in use: keeps going
	now = now.Add(30 * time.Minute)
	rec.Seen(ctx, uid, "10.0.0.1") // back in the menu after a long silence: does not count

	rec.Open(ctx, uid, "jan", "10.0.0.2", "VLC", 0, "Some film", store.KindVOD)
	rec.Open(ctx, uid, "jan", "10.0.0.2", "VLC", 0, "Other film", store.KindVOD) // zapping straight away

	views, err := st.RecentViews(ctx, store.ViewFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 4 {
		t.Fatalf("views = %+v", views)
	}
	want := map[string]time.Duration{"NH": 90 * time.Minute, "Gld": 24 * time.Minute, "Some film": 0, "Other film": Assume}
	for _, v := range views {
		if v.Duration() != want[v.Channel] {
			t.Errorf("%s lasted %s, want %s", v.Channel, v.Duration(), want[v.Channel])
		}
	}

	// A channel opened the next morning does not stretch last night's view.
	now = start.Add(14 * time.Hour)
	rec.Open(ctx, uid, "jan", "10.0.0.1", "MyTVOnline", nh, "NH", store.KindTV)
	views, _ = st.RecentViews(ctx, store.ViewFilter{ChannelID: gld}, 10)
	if len(views) != 1 || views[0].Duration() != 24*time.Minute {
		t.Fatalf("Gld after the night = %+v", views)
	}

	// Retention.
	if err := st.SetViewRetention(ctx, 1); err != nil {
		t.Fatal(err)
	}
	now = start.Add(36 * time.Hour)
	if err := rec.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if views, _ = st.RecentViews(ctx, store.ViewFilter{}, 10); len(views) != 1 || views[0].Channel != "NH" {
		t.Fatalf("after purge = %+v", views)
	}
	if err := st.SetViewRetention(ctx, -1); err == nil {
		t.Fatal("negative retention accepted")
	}
}
