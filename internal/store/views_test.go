package store

import (
	"context"
	"testing"
	"time"
)

func TestViewStatistics(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	jan, _ := s.SaveUser(ctx, User{Username: "jan", Enabled: true}, "password123")
	piet, _ := s.SaveUser(ctx, User{Username: "piet", Enabled: true}, "password123")
	nh, _ := s.SaveChannel(ctx, Channel{Name: "NH", StreamURL: "https://example.com/nh.m3u8", Enabled: true})
	radio, _ := s.SaveChannel(ctx, Channel{Name: "Radio 1", Kind: KindRadio, StreamURL: "https://example.com/r1.mp3", Enabled: true})
	idle, _ := s.SaveChannel(ctx, Channel{Name: "Idle", StreamURL: "https://example.com/idle.m3u8", Enabled: true})
	s.SaveChannel(ctx, Channel{Name: "Off", StreamURL: "https://example.com/off.m3u8"})

	now := time.Now()
	add := func(user int64, name string, channel int64, kind, title string, ago, d time.Duration) {
		t.Helper()
		start := now.Add(-ago)
		if _, err := s.StartView(ctx, View{UserID: user, Username: name, ChannelID: channel, Channel: title, Kind: kind,
			StartedAt: start, EndedAt: start.Add(d)}); err != nil {
			t.Fatal(err)
		}
	}
	add(jan, "jan", nh, KindTV, "NH", time.Hour, 30*time.Minute)
	add(jan, "jan", nh, KindTV, "NH", 2*time.Hour, 30*time.Minute)
	add(jan, "jan", radio, KindRadio, "Radio 1", 3*time.Hour, 10*time.Minute)
	add(piet, "piet", nh, KindTV, "NH", 4*time.Hour, 5*time.Minute)
	add(piet, "piet", 0, KindVOD, "Film", 5*time.Hour, 90*time.Minute)
	add(piet, "piet", idle, KindTV, "Idle", 40*24*time.Hour, time.Minute) // before the period

	f := ViewFilter{Since: now.AddDate(0, 0, -30)}
	sum, err := s.ViewSummary(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Views != 5 || sum.Viewers != 2 || sum.Channels != 3 || sum.Time != 165*time.Minute {
		t.Fatalf("summary = %+v", sum)
	}
	chs, err := s.ChannelViews(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 3 || chs[0].Name != "Film" || chs[1].Name != "NH" || chs[1].Views != 3 || chs[1].Viewers != 2 ||
		chs[1].Time != 65*time.Minute || chs[0].ChannelID != 0 || chs[1].ChannelID != nh {
		t.Fatalf("channels = %+v", chs)
	}
	users, err := s.UserViews(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Username != "piet" || users[0].Favourite != "Film" || users[1].Favourite != "NH" ||
		users[1].Views != 3 || users[1].Channels != 2 {
		t.Fatalf("users = %+v", users)
	}
	if radioOnly, _ := s.ChannelViews(ctx, ViewFilter{Since: f.Since, Kind: KindRadio}); len(radioOnly) != 1 {
		t.Fatalf("radio = %+v", radioOnly)
	}
	un, err := s.UnwatchedChannels(ctx, "", f.Since)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 1 || un[0].ID != idle || un[0].LastView == nil {
		t.Fatalf("unwatched = %+v", un)
	}
	days, err := s.ViewsByDay(ctx, ViewFilter{Since: now.AddDate(0, 0, -6)}, now)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, d := range days {
		total += d.Views
	}
	if len(days) != 7 || total != 5 {
		t.Fatalf("days = %+v", days)
	}

	// Renaming a channel shows the new name; deleting a user removes their
	// views, deleting a channel keeps them under its old name.
	c, _ := s.Channel(ctx, nh)
	c.Name = "NH Nieuws"
	s.SaveChannel(ctx, c)
	if err := s.DeleteUser(ctx, piet); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(ctx, radio); err != nil {
		t.Fatal(err)
	}
	chs, _ = s.ChannelViews(ctx, f)
	if len(chs) != 2 || chs[0].Name != "NH Nieuws" || chs[0].Views != 2 || chs[1].Name != "Radio 1" || chs[1].ChannelID != 0 {
		t.Fatalf("after deletes = %+v", chs)
	}
	if err := s.ClearViews(ctx); err != nil {
		t.Fatal(err)
	}
	if sum, _ := s.ViewSummary(ctx, ViewFilter{}); sum.Views != 0 {
		t.Fatal("views left after clearing")
	}
}
