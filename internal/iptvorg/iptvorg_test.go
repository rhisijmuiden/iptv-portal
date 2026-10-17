package iptvorg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

var testFiles = map[string]string{
	"channels.json": `[
		{"id":"NPO1.nl","name":"NPO 1","country":"NL","categories":["general"],"is_nsfw":false,"closed":null},
		{"id":"Old.nl","name":"Old","country":"NL","categories":[],"is_nsfw":false,"closed":"2020-01-01"},
		{"id":"BBC1.uk","name":"BBC One","country":"UK","categories":["general","news"],"is_nsfw":false,"closed":null}
	]`,
	"streams.json": `[
		{"channel":"NPO1.nl","feed":"SD","title":"NPO 1","url":"https://s/npo1.m3u8","quality":"720p","labels":[],"user_agent":null,"referrer":null},
		{"channel":"NPO1.nl","feed":null,"title":"NPO 1","url":"https://s/npo1-hd.m3u8","quality":"1080p","labels":["Geo-blocked"],"user_agent":"x","referrer":null},
		{"channel":"Old.nl","feed":null,"title":"Old","url":"https://s/old.m3u8","quality":null,"labels":[],"user_agent":null,"referrer":null},
		{"channel":"BBC1.uk","feed":null,"title":"BBC One","url":"https://s/bbc1.m3u8","quality":null,"labels":[],"user_agent":null,"referrer":null},
		{"channel":null,"feed":null,"title":"Loose","url":"https://s/loose.m3u8","quality":null,"labels":[],"user_agent":null,"referrer":null}
	]`,
	"logos.json": `[
		{"channel":"NPO1.nl","feed":"SD","in_use":true,"url":"https://l/npo1-sd.png"},
		{"channel":"NPO1.nl","feed":null,"in_use":true,"url":"https://l/npo1.png"},
		{"channel":"BBC1.uk","feed":"HD","in_use":true,"url":"https://l/bbc1-hd.png"}
	]`,
	"countries.json":  `[{"code":"NL","name":"Netherlands","flag":"🇳🇱"},{"code":"UK","name":"United Kingdom","flag":"🇬🇧"},{"code":"BE","name":"Belgium","flag":"🇧🇪"}]`,
	"categories.json": `[{"id":"general","name":"General"},{"id":"news","name":"News"}]`,
}

func testClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := testFiles[r.URL.Path[1:]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	return c
}

func TestCatalog(t *testing.T) {
	cat, err := testClient(t).Catalog(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Channels) != 3 {
		t.Fatalf("channels = %+v", cat.Channels)
	}
	if len(cat.Countries) != 2 || cat.Countries[0].Code != "NL" || cat.Countries[0].Count != 1 {
		t.Fatalf("countries = %+v", cat.Countries)
	}
	nl, more := cat.Search(Filter{Country: "nl"})
	if more || len(nl) != 1 {
		t.Fatalf("NL = %+v", nl)
	}
	npo := nl[0]
	if npo.Logo != "https://l/npo1.png" || len(npo.Streams) != 2 {
		t.Fatalf("NPO 1 = %+v", npo)
	}
	if id := npo.EPGID(npo.Streams[0]); id != "NPO1.nl@SD" {
		t.Fatalf("EPG id = %q", id)
	}
	if !npo.Streams[1].NeedsHeaders || npo.Streams[0].NeedsHeaders {
		t.Fatalf("streams = %+v", npo.Streams)
	}
	if news, _ := cat.Search(Filter{Category: "news"}); len(news) != 1 || news[0].Logo != "https://l/bbc1-hd.png" {
		t.Fatalf("news = %+v", news)
	}
	if found, _ := cat.Search(Filter{Search: "bbc"}); len(found) != 1 {
		t.Fatalf("search = %+v", found)
	}
	if all, more := cat.Search(Filter{Limit: 2}); len(all) != 2 || !more {
		t.Fatalf("limit = %d, %v", len(all), more)
	}
	ch, s, ok := cat.Find("https://s/npo1-hd.m3u8")
	if !ok || ch.ID != "NPO1.nl" || s.Quality != "1080p" {
		t.Fatalf("find = %+v %+v %v", ch, s, ok)
	}
	if cat.CategoryName("news") != "News" {
		t.Fatal("category name")
	}
}

func TestCatalogDownloadError(t *testing.T) {
	c := testClient(t)
	c.BaseURL += "/missing"
	if _, err := c.Catalog(context.Background(), false); err == nil {
		t.Fatal("expected an error")
	}
}
