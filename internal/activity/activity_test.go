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

func TestOpenLimit(t *testing.T) {
	now := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	tr := New()
	tr.now = func() time.Time { return now }

	if !tr.Open(1, "jan", "10.0.0.1", "", 7, "NH", 2) || !tr.Open(1, "jan", "10.0.0.2", "", 7, "NH", 2) {
		t.Fatal("first two places refused")
	}
	tr.Seen(1, "jan", "10.0.0.3", "") // browsing without watching holds no slot
	if tr.ActiveConnections(1) != 2 {
		t.Fatalf("active = %d, want 2", tr.ActiveConnections(1))
	}
	if tr.Open(1, "jan", "10.0.0.3", "", 8, "Gld", 2) {
		t.Fatal("third place allowed")
	}
	if !tr.Open(1, "jan", "10.0.0.1", "", 8, "Gld", 2) {
		t.Fatal("channel switch refused")
	}
	if !tr.Open(2, "piet", "10.0.0.9", "", 7, "NH", 1) || !tr.Open(1, "jan", "10.0.0.4", "", 7, "NH", 0) {
		t.Fatal("other user or no limit refused")
	}

	// A place that keeps using the portal keeps its slot; a silent one
	// frees it after the online window.
	now = now.Add(10 * time.Minute)
	tr.Seen(1, "jan", "10.0.0.1", "")
	now = now.Add(10 * time.Minute)
	tr.Seen(1, "jan", "10.0.0.1", "")
	if got := tr.ActiveConnections(1); got != 1 {
		t.Fatalf("active after 20 minutes = %d, want 1", got)
	}
	if !tr.Open(1, "jan", "10.0.0.3", "", 8, "Gld", 2) {
		t.Fatal("place refused after a slot freed up")
	}

	tr.Release(1)
	if got := tr.ActiveConnections(1); got != 0 {
		t.Fatalf("active after release = %d", got)
	}
}
