package store

import (
	"context"
	"testing"
)

func TestHealthSettings(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	hs, err := st.HealthSettings(ctx)
	if err != nil || hs != DefaultHealthSettings {
		t.Fatalf("defaults: %+v %v", hs, err)
	}
	for _, bad := range []HealthSettings{{IntervalMinutes: 1}, {IntervalMinutes: MaxHealthInterval + 1}, {DisableAfter: -1}} {
		if err := st.SaveHealthSettings(ctx, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	want := HealthSettings{IntervalMinutes: 0, DisableAfter: 3}
	if err := st.SaveHealthSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if hs, _ := st.HealthSettings(ctx); hs != want {
		t.Errorf("got %+v, want %+v", hs, want)
	}
	// The general settings are untouched by and do not touch these keys.
	s, _ := st.Settings(ctx)
	if err := st.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
	if hs, _ := st.HealthSettings(ctx); hs != want {
		t.Errorf("after saving settings: %+v", hs)
	}
}

func TestRecordHealth(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	id, err := st.SaveChannel(ctx, Channel{Name: "NH", StreamURL: "https://example.com/nh.m3u8", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, change, err := st.RecordHealth(ctx, id, false, "HTTP 404", 0); err != nil || change != HealthUnchanged {
		t.Fatalf("without auto-disable: %v %v", change, err)
	}
	h, change, err := st.RecordHealth(ctx, id, false, "HTTP 404", 2)
	if err != nil || change != HealthDisabled || h.Failures != 2 || !h.AutoDisabled {
		t.Fatalf("second failure: %+v %v %v", h, change, err)
	}
	if ch, _ := st.Channel(ctx, id); ch.Enabled {
		t.Error("channel still enabled")
	}
	h, change, err = st.RecordHealth(ctx, id, true, "ok", 2)
	if err != nil || change != HealthReenabled || h.Failures != 0 || h.AutoDisabled || h.LastOKAt == nil {
		t.Fatalf("recovery: %+v %v %v", h, change, err)
	}
	if ch, _ := st.Channel(ctx, id); !ch.Enabled {
		t.Error("channel not enabled again")
	}

	// Cleared by hand: the checker leaves the channel alone.
	st.RecordHealth(ctx, id, false, "x", 1)
	if err := st.ClearAutoDisabled(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, change, _ := st.RecordHealth(ctx, id, true, "ok", 1); change != HealthUnchanged {
		t.Errorf("re-enabled a channel cleared by hand: %v", change)
	}

	if err := st.DeleteChannel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.ChannelHealth(ctx); len(all) != 0 {
		t.Errorf("health of a deleted channel kept: %v", all)
	}
	if _, _, err := st.RecordHealth(ctx, id, true, "ok", 0); err != ErrNotFound {
		t.Errorf("deleted channel: %v", err)
	}
}
