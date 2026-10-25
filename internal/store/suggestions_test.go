package store

import (
	"context"
	"testing"
)

func TestSuggestions(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	id, err := st.SaveChannel(ctx, Channel{Name: "NH", StreamURL: "https://example.com/dead.m3u8", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RecordHealth(ctx, id, false, "HTTP 404 Not Found", 1); err != nil {
		t.Fatal(err)
	}
	list := []Suggestion{
		{StreamURL: "https://a.example/nh.m3u8", Name: "NH (720p)", Source: SourceIPTVOrg, SourceID: "NH.nl",
			LogoURL: "https://a.example/nh.png", EPGID: "NH.nl@SD", Match: MatchEPG, Detail: "HLS, segments load"},
		{StreamURL: "https://b.example/nh.m3u8", Name: "NH", Source: SourceIPTVOrg, Match: MatchName},
	}
	if n, err := st.SaveSuggestions(ctx, id, list); err != nil || n != 2 {
		t.Fatalf("saved %d, %v", n, err)
	}
	// Saving again adds nothing and notes the search.
	if n, err := st.SaveSuggestions(ctx, id, list); err != nil || n != 0 {
		t.Fatalf("saved again %d, %v", n, err)
	}
	if searched, _ := st.SearchedAt(ctx); searched[id].IsZero() {
		t.Error("search not noted")
	}
	sugs, err := st.Suggestions(ctx)
	if err != nil || len(sugs[id]) != 2 {
		t.Fatalf("suggestions %+v %v", sugs, err)
	}
	if n, _ := st.CountSuggestions(ctx); n != 1 {
		t.Errorf("count %d, want 1 channel", n)
	}

	// A dismissed stream stays dismissed when it is found again.
	if _, err := st.DismissSuggestion(ctx, sugs[id][1].ID); err != nil {
		t.Fatal(err)
	}
	st.SaveSuggestions(ctx, id, list)
	sugs, _ = st.Suggestions(ctx)
	if len(sugs[id]) != 1 {
		t.Fatalf("after dismiss: %+v", sugs[id])
	}
	if d, _ := st.DismissedStreams(ctx, id); !d["https://b.example/nh.m3u8"] {
		t.Errorf("dismissed streams %v", d)
	}

	c, err := st.AcceptSuggestion(ctx, sugs[id][0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.StreamURL != "https://a.example/nh.m3u8" || c.LogoURL != "https://a.example/nh.png" || c.EPGID != "NH.nl@SD" {
		t.Errorf("accepted channel %+v", c)
	}
	if got, _ := st.Channel(ctx, id); got.StreamURL != c.StreamURL {
		t.Errorf("stored channel %+v", got)
	}
	if sugs, _ := st.Suggestions(ctx); len(sugs) != 0 {
		t.Errorf("open suggestions left: %+v", sugs)
	}
	if _, err := st.AcceptSuggestion(ctx, list[0].ID); err != ErrNotFound {
		t.Errorf("accepting a gone suggestion: %v", err)
	}
}

func TestSuggestionsClearedWhenWorking(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	id, _ := st.SaveChannel(ctx, Channel{Name: "R", Kind: KindRadio, StreamURL: "https://example.com/r.mp3", Enabled: true,
		SourceID: "old-uuid"})
	st.RecordHealth(ctx, id, false, "HTTP 404 Not Found", 0)
	st.SaveSuggestions(ctx, id, []Suggestion{{StreamURL: "https://example.com/r2.mp3", Source: SourceRadioBrowser,
		SourceID: "new-uuid", Match: MatchName}})
	st.RecordHealth(ctx, id, true, "stream responds", 0)
	if sugs, _ := st.Suggestions(ctx); len(sugs) != 0 {
		t.Errorf("suggestions kept for a working channel: %+v", sugs)
	}
	if searched, _ := st.SearchedAt(ctx); !searched[id].IsZero() {
		t.Error("search time kept for a working channel")
	}

	// Accepting a station takes over its Radio Browser UUID.
	st.RecordHealth(ctx, id, false, "HTTP 404 Not Found", 0)
	st.SaveSuggestions(ctx, id, []Suggestion{{StreamURL: "https://example.com/r2.mp3", Source: SourceRadioBrowser,
		SourceID: "new-uuid", Match: MatchName}})
	sugs, _ := st.Suggestions(ctx)
	c, err := st.AcceptSuggestion(ctx, sugs[id][0].ID)
	if err != nil || c.SourceID != "new-uuid" || c.StreamURL != "https://example.com/r2.mp3" {
		t.Errorf("accepted station %+v %v", c, err)
	}
}
