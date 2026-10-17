package store

import (
	"context"
	"testing"
)

func TestMediaServersAndOnDemand(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.SaveMediaServer(ctx, MediaServer{Name: "Home", Kind: MediaPlex, URL: "http://plex:32400/"}); err == nil {
		t.Fatal("server without token accepted")
	}
	id, err := s.SaveMediaServer(ctx, MediaServer{Name: "Home", Kind: MediaPlex, URL: "http://plex:32400/",
		Token: "tok", Libraries: []string{"1", "2"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// An empty token on update keeps the stored one.
	if _, err := s.SaveMediaServer(ctx, MediaServer{ID: id, Name: "Home", Kind: MediaPlex, URL: "http://plex:32400",
		Libraries: []string{"2"}, Direct: true}); err != nil {
		t.Fatal(err)
	}
	m, err := s.MediaServer(ctx, id)
	if err != nil || m.Token != "tok" || m.URL != "http://plex:32400" || !m.HasLibrary("2") || m.HasLibrary("1") ||
		!m.Direct || m.Enabled {
		t.Fatalf("server = %+v, %v", m, err)
	}

	ids, err := s.MediaIDs(ctx, id, []string{"movie:a", "movie:b"})
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.MediaIDs(ctx, id, []string{"movie:b", "movie:c"})
	if ids["movie:b"] == 0 || again["movie:b"] != ids["movie:b"] || again["movie:c"] == ids["movie:a"] {
		t.Fatalf("ids not stable: %v %v", ids, again)
	}

	vod, err := s.SavePackage(ctx, Package{Name: "Films", OnDemand: true})
	if err != nil {
		t.Fatalf("On Demand only package: %v", err)
	}
	if p, _ := s.Package(ctx, vod); !p.OnDemand {
		t.Fatal("OnDemand not stored")
	}
	with, _ := s.SaveUser(ctx, User{Username: "piet", Enabled: true, PackageIDs: []int64{vod}}, "secret1")
	without, _ := s.SaveUser(ctx, User{Username: "jan", Enabled: true}, "secret1")
	if ok, _ := s.UserOnDemand(ctx, with); !ok {
		t.Error("user with On Demand package denied")
	}
	if ok, _ := s.UserOnDemand(ctx, without); ok {
		t.Error("user without On Demand package allowed")
	}

	if err := s.DeleteMediaServer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.MediaIDs(ctx, id, nil); len(left) != 0 {
		t.Errorf("ids left after delete: %v", left)
	}
}
