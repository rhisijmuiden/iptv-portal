package store

import (
	"context"
	"testing"
)

func TestBulkChannels(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	add := func(kind, name string) int64 {
		t.Helper()
		id, err := s.SaveChannel(ctx, Channel{Kind: kind, Name: name, StreamURL: "https://example.com/" + name, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c := add(KindTV, "a"), add(KindTV, "b"), add(KindTV, "c")
	r := add(KindRadio, "r")
	news, _ := s.EnsureCategory(ctx, KindTV, "News")
	music, _ := s.EnsureCategory(ctx, KindRadio, "Music")

	// A radio station among TV ids is left alone.
	if n, err := s.SetChannelsCategory(ctx, KindTV, []int64{a, b, r}, news); err != nil || n != 2 {
		t.Fatalf("move: %d, %v", n, err)
	}
	if ch, _ := s.Channel(ctx, r); ch.CategoryID != 0 {
		t.Errorf("radio station moved to %d", ch.CategoryID)
	}
	if ch, _ := s.Channel(ctx, b); ch.CategoryID != news {
		t.Errorf("b in category %d", ch.CategoryID)
	}
	if _, err := s.SetChannelsCategory(ctx, KindTV, []int64{a}, music); err == nil {
		t.Error("TV channel moved to a radio category")
	}
	if n, err := s.SetChannelsCategory(ctx, KindTV, []int64{a}, 0); err != nil || n != 1 {
		t.Fatalf("clear category: %d, %v", n, err)
	}
	if ch, _ := s.Channel(ctx, a); ch.CategoryID != 0 {
		t.Errorf("a still in category %d", ch.CategoryID)
	}

	// Switching by hand clears the automatic switch-off.
	if _, _, err := s.RecordHealth(ctx, c, false, "dead", 1); err != nil {
		t.Fatal(err)
	}
	if hl, _ := s.ChannelHealth(ctx); !hl[c].AutoDisabled {
		t.Fatal("c not switched off automatically")
	}
	if n, err := s.SetChannelsEnabled(ctx, KindTV, []int64{b, c}, false); err != nil || n != 2 {
		t.Fatalf("disable: %d, %v", n, err)
	}
	if hl, _ := s.ChannelHealth(ctx); hl[c].AutoDisabled {
		t.Error("c still marked as switched off automatically")
	}
	if ch, _ := s.Channel(ctx, b); ch.Enabled {
		t.Error("b still enabled")
	}

	if n, err := s.DeleteChannels(ctx, KindTV, []int64{a, c, r}); err != nil || n != 2 {
		t.Fatalf("delete: %d, %v", n, err)
	}
	if _, err := s.Channel(ctx, r); err != nil {
		t.Errorf("radio station deleted: %v", err)
	}
	if left, _ := s.Channels(ctx, ChannelFilter{Kind: KindTV}); len(left) != 1 || left[0].ID != b {
		t.Errorf("left = %+v", left)
	}
	if n, err := s.DeleteChannels(ctx, KindTV, nil); err != nil || n != 0 {
		t.Errorf("empty delete: %d, %v", n, err)
	}
}
