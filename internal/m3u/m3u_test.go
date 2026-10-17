package m3u

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := "\ufeff#EXTM3U x-tvg-url=\"http://epg\"\n" +
		`#EXTINF:-1 tvg-id="nh.nl" tvg-logo="http://logo/nh.png" group-title="Noord-Holland",NH Nieuws` + "\n" +
		"#EXTVLCOPT:http-user-agent=foo\n" +
		"https://stream.example/nh/index.m3u8\n" +
		"\n" +
		`#EXTINF:-1 group-title="Regio, Oost",Omroep Gelderland, HD` + "\n" +
		"https://stream.example/gld.m3u8\n" +
		"#EXTINF:0,Lokaal\n" +
		"#EXTGRP:Lokaal\n" +
		"http://stream.example/lokaal.m3u8\n" +
		"http://stream.example/kaal.m3u8\n"

	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(got), got)
	}
	checks := []struct {
		name, url, id, logo, group string
	}{
		{"NH Nieuws", "https://stream.example/nh/index.m3u8", "nh.nl", "http://logo/nh.png", "Noord-Holland"},
		{"Omroep Gelderland, HD", "https://stream.example/gld.m3u8", "", "", "Regio, Oost"},
		{"Lokaal", "http://stream.example/lokaal.m3u8", "", "", "Lokaal"},
		{"http://stream.example/kaal.m3u8", "http://stream.example/kaal.m3u8", "", "", ""},
	}
	for i, c := range checks {
		e := got[i]
		if e.Name != c.name || e.URL != c.url || e.Attr("tvg-id") != c.id ||
			e.Attr("tvg-logo") != c.logo || e.Attr("group-title") != c.group {
			t.Errorf("entry %d = %+v, want %+v", i, e, c)
		}
	}
}
