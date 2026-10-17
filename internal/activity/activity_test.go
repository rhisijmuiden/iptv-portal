package activity

import (
	"testing"
	"time"
)

func TestTracker(t *testing.T) {
	now := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	tr := New()
	tr.now = func() time.Time { return now }

	tr.Seen(1, "jan", "10.0.0.1", "MyTVOnline")
	tr.Play(1, "jan", "10.0.0.1", "", 7, "NH")
	tr.Seen(1, "jan", "10.0.0.2", "TiviMate") // second device of the same user
	tr.Seen(2, "piet", "10.0.0.3", "VLC")

	now = now.Add(20 * time.Minute)
	tr.Seen(2, "piet", "10.0.0.3", "VLC")
	tr.Seen(3, "old", "10.0.0.4", "")
	tr.now = func() time.Time { return now.Add(-25 * time.Hour) }
	tr.Seen(3, "old", "10.0.0.4", "") // backdated: should be forgotten
	tr.now = func() time.Time { return now }

	s := tr.Snapshot()
	if s.OnlineUsers != 1 || s.Watching != 0 {
		t.Fatalf("online %d watching %d, want 1 0", s.OnlineUsers, s.Watching)
	}
	if len(s.Connections) != 3 || s.Connections[0].Username != "piet" {
		t.Fatalf("connections = %+v", s.Connections)
	}
	var jan Connection
	for _, c := range s.Connections {
		if c.IP == "10.0.0.1" {
			jan = c
		}
	}
	if jan.Channel != "NH" || jan.ChannelID != 7 || jan.UserAgent != "MyTVOnline" {
		t.Fatalf("jan = %+v", jan)
	}

	now = now.Add(time.Minute)
	tr.Play(1, "jan", "10.0.0.2", "TiviMate", 8, "Gld")
	s = tr.Snapshot()
	if s.OnlineUsers != 2 || s.Watching != 1 || s.Connections[0].Channel != "Gld" {
		t.Fatalf("after play: %+v", s)
	}
}
