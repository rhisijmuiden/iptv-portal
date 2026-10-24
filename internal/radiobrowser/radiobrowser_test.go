package radiobrowser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.URL.RequestURI())
		if !strings.HasPrefix(r.UserAgent(), "iptv-portal/") {
			t.Errorf("User-Agent = %q", r.UserAgent())
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/json/stations/search", "/json/stations/byuuid":
			w.Write([]byte(`[
				{"stationuuid":"u1","name":" NPO Radio 1 ","url":"http://x/radio1.pls","url_resolved":"https://icecast/radio1.mp3",
				 "homepage":"https://www.nporadio1.nl","favicon":"https://x/r1.png","tags":"news,talk,News",
				 "country":"The Netherlands","countrycode":"nl","language":"dutch","codec":"MP3","bitrate":192,
				 "votes":900,"clickcount":40,"hls":0,"lastcheckok":1},
				{"stationuuid":"u2","name":"Only url","url":"https://y/stream","url_resolved":"","tags":"","hls":1,"lastcheckok":0},
				{"stationuuid":"","name":"No uuid"}
			]`))
		case "/json/countries":
			w.Write([]byte(`[{"name":"The Netherlands","iso_3166_1":"NL","stationcount":800},{"name":"Germany","iso_3166_1":"DE","stationcount":5000},{"name":"Nowhere","iso_3166_1":"","stationcount":3}]`))
		case "/json/tags":
			w.Write([]byte(`[{"name":"pop","stationcount":9000},{"name":"news","stationcount":4000}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSearch(t *testing.T) {
	var seen []string
	srv := fakeServer(t, &seen)
	c := NewClient()
	c.BaseURL = srv.URL
	got, err := c.Search(context.Background(), Filter{Name: "npo", CountryCode: "nl", Tag: "news", Language: "dutch", Limit: 50, Offset: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d stations, want 2", len(got))
	}
	s := got[0]
	if s.Name != "NPO Radio 1" || s.URL != "https://icecast/radio1.mp3" || s.CountryCode != "NL" ||
		strings.Join(s.Tags, ",") != "news,talk" || s.Bitrate != 192 || !s.Working || s.HLS {
		t.Errorf("station = %+v", s)
	}
	if got[1].URL != "https://y/stream" || !got[1].HLS || got[1].Working {
		t.Errorf("second station = %+v", got[1])
	}
	q := seen[0]
	for _, want := range []string{"name=npo", "countrycode=NL", "tag=news", "tagExact=true", "language=dutch",
		"order=votes", "reverse=true", "limit=50", "offset=100", "hidebroken=true"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q lacks %q", q, want)
		}
	}
}

func TestByUUIDBatches(t *testing.T) {
	var seen []string
	srv := fakeServer(t, &seen)
	c := NewClient()
	c.BaseURL = srv.URL
	ids := make([]string, 120)
	for i := range ids {
		ids[i] = "id"
	}
	if _, err := c.ByUUID(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Errorf("%d requests, want 3", len(seen))
	}
}

func TestListsAreCached(t *testing.T) {
	var seen []string
	srv := fakeServer(t, &seen)
	c := NewClient()
	c.BaseURL = srv.URL
	for range 2 {
		cs, err := c.Countries(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) != 2 || cs[0].Code != "DE" || cs[1].Name != "The Netherlands" {
			t.Errorf("countries = %+v", cs)
		}
	}
	tags, err := c.Tags(context.Background())
	if err != nil || len(tags) != 2 || tags[0].Name != "pop" {
		t.Errorf("tags = %+v, %v", tags, err)
	}
	if len(seen) != 2 {
		t.Errorf("%d requests, want 2 (countries once, tags once)", len(seen))
	}
}

func TestSearchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient()
	c.BaseURL = srv.URL
	if _, err := c.Search(context.Background(), Filter{}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v", err)
	}
}

func TestResolvePlaylist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.pls":
			w.Write([]byte("[playlist]\nNumberOfEntries=1\nFile1=http://stream.example/live.mp3\nTitle1=Live\n"))
		case "/b.m3u":
			w.Write([]byte("#EXTM3U\n#EXTINF:-1,Live\nhttps://stream.example/b.aac\n"))
		case "/c.asx":
			w.Write([]byte(`<asx version="3.0"><entry><ref href="http://stream.example/c.wma"/></entry></asx>`))
		case "/empty.pls":
			w.Write([]byte("[playlist]\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cases := map[string]string{
		srv.URL + "/a.pls":        "http://stream.example/live.mp3",
		srv.URL + "/b.m3u":        "https://stream.example/b.aac",
		srv.URL + "/c.asx":        "http://stream.example/c.wma",
		srv.URL + "/empty.pls":    srv.URL + "/empty.pls",
		srv.URL + "/missing.pls":  srv.URL + "/missing.pls",
		srv.URL + "/live.m3u8":    srv.URL + "/live.m3u8",
		"https://icecast/x.mp3":   "https://icecast/x.mp3",
		"https://icecast/noext/;": "https://icecast/noext/;",
	}
	for in, want := range cases {
		if got := ResolvePlaylist(context.Background(), srv.Client(), in); got != want {
			t.Errorf("ResolvePlaylist(%q) = %q, want %q", in, got, want)
		}
	}
}
