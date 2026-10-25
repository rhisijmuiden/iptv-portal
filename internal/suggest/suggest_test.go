package suggest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/iptvorg"
	"github.com/rhisijmuiden/iptv-portal/internal/radiobrowser"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

var iptvFiles = map[string]string{
	"channels.json": `[
		{"id":"RTVNoord.nl","name":"RTV Noord","country":"NL","categories":["general"],"closed":null},
		{"id":"RTVNoordBE.be","name":"RTV Noord","country":"BE","categories":[],"closed":null},
		{"id":"NPO1.nl","name":"NPO 1","country":"NL","categories":[],"closed":null},
		{"id":"NPO1Extra.nl","name":"NPO 1 Extra","country":"NL","categories":[],"closed":null}
	]`,
	"streams.json": `[
		{"channel":"RTVNoord.nl","feed":"SD","title":"RTV Noord","url":"https://s/noord-dead.m3u8","quality":"576p","labels":[]},
		{"channel":"RTVNoord.nl","feed":"HD","title":"RTV Noord","url":"https://s/noord-hd.m3u8","quality":"1080p","labels":[]},
		{"channel":"RTVNoord.nl","feed":"SD","title":"RTV Noord","url":"https://s/noord-sd.m3u8","quality":"576p","labels":["Geo-blocked"]},
		{"channel":"RTVNoord.nl","feed":"SD","title":"RTV Noord","url":"https://s/noord-ua.m3u8","quality":"576p","labels":[],"user_agent":"Mozilla"},
		{"channel":"RTVNoordBE.be","feed":null,"title":"RTV Noord","url":"https://s/noord-be.m3u8","quality":null,"labels":[]},
		{"channel":"NPO1.nl","feed":null,"title":"NPO 1","url":"https://s/npo1-a.m3u8","quality":"720p","labels":[]},
		{"channel":"NPO1.nl","feed":null,"title":"NPO 1","url":"https://s/npo1-b.m3u8","quality":"1080p","labels":[]},
		{"channel":"NPO1Extra.nl","feed":null,"title":"NPO 1 Extra","url":"https://s/npo1x.m3u8","quality":null,"labels":[]}
	]`,
	"logos.json":      `[{"channel":"RTVNoord.nl","feed":null,"in_use":true,"url":"https://l/noord.png"}]`,
	"countries.json":  `[{"code":"NL","name":"Netherlands","flag":""},{"code":"BE","name":"Belgium","flag":""}]`,
	"categories.json": `[{"id":"general","name":"General"}]`,
}

func iptvClient(t *testing.T) *iptvorg.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := iptvFiles[r.URL.Path[1:]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := iptvorg.NewClient()
	c.BaseURL = srv.URL
	return c
}

func radioClient(t *testing.T) *radiobrowser.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/stations/byuuid":
			if r.URL.Query().Get("uuids") == "uuid-1" {
				w.Write([]byte(`[{"stationuuid":"uuid-1","name":"Radio Noord","url":"https://r/old.pls","url_resolved":"https://r/noord-new.mp3","codec":"MP3","bitrate":128,"countrycode":"nl","lastcheckok":1}]`))
				return
			}
			w.Write([]byte(`[]`))
		case "/json/stations/search":
			if !strings.EqualFold(r.URL.Query().Get("name"), "Radio Noord") {
				w.Write([]byte(`[]`))
				return
			}
			w.Write([]byte(`[
				{"stationuuid":"uuid-1","name":"Radio Noord","url_resolved":"https://r/noord-new.mp3","lastcheckok":1},
				{"stationuuid":"uuid-2","name":"Radio Noord (AAC)","url_resolved":"https://r/noord-aac.aac","codec":"AAC","lastcheckok":1},
				{"stationuuid":"uuid-3","name":"Radio Noord Oldies","url_resolved":"https://r/oldies.mp3","lastcheckok":1}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := radiobrowser.NewClient()
	c.BaseURL = srv.URL
	return c
}

// fakeProbe plays only the listed URLs and remembers what was tested.
type fakeProbe struct {
	mu     sync.Mutex
	plays  map[string]bool
	tested []string
}

func (p *fakeProbe) probe(_ context.Context, u string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tested = append(p.tested, u)
	if p.plays[u] {
		return "HLS, segments load", nil
	}
	return "", errors.New("HTTP 404 Not Found")
}

func TestNormName(t *testing.T) {
	for in, want := range map[string]string{
		"NPO 1":           "npo1",
		"NPO1 HD":         "npo1",
		"NPO 1 (720p)":    "npo1",
		"RTV Noord [Geo]": "rtvnoord",
		"Radio Noord AAC": "radionoord",
		"Omrop Fryslân":   "omropfryslân",
		"NPO 1 Extra":     "npo1extra",
		"(only brackets)": "",
	} {
		if got := normName(in); got != want {
			t.Errorf("normName(%q) = %q, want %q", in, got, want)
		}
	}
	if id, feed := splitEPGID("NPO1.nl@HD"); id != "NPO1.nl" || feed != "HD" {
		t.Errorf("splitEPGID: %q %q", id, feed)
	}
	if c := countryOf("NPO1.nl"); c != "nl" {
		t.Errorf("countryOf: %q", c)
	}
}

func TestTVCandidates(t *testing.T) {
	cat, err := iptvClient(t).Catalog(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	urls := func(list []store.Suggestion) string {
		var out []string
		for _, s := range list {
			out = append(out, strings.TrimPrefix(s.StreamURL, "https://s/")+"/"+s.Match)
		}
		return strings.Join(out, " ")
	}
	// By EPG ID: the same feed first, the stream that needs headers left out.
	got := tvCandidates(cat, store.Channel{Name: "Noord", EPGID: "RTVNoord.nl@SD", StreamURL: "https://x/dead.m3u8"})
	if want := "noord-dead.m3u8/epg noord-sd.m3u8/epg noord-hd.m3u8/epg"; urls(got) != want {
		t.Errorf("by EPG ID: %s\nwant %s", urls(got), want)
	}
	if got[1].LogoURL != "https://l/noord.png" || got[1].EPGID != "RTVNoord.nl@SD" || got[1].Name != "RTV Noord (SD, 576p, Geo-blocked)" {
		t.Errorf("details %+v", got[1])
	}
	// By the dead stream: the channel listing it, then same-name channels in
	// the same country only.
	got = tvCandidates(cat, store.Channel{Name: "RTV Noord", StreamURL: "https://s/noord-dead.m3u8"})
	if want := "noord-dead.m3u8/url noord-hd.m3u8/url noord-sd.m3u8/url"; urls(got) != want {
		t.Errorf("by stream: %s\nwant %s", urls(got), want)
	}
	// By name only: every country, but not a different channel.
	got = tvCandidates(cat, store.Channel{Name: "NPO 1 HD", StreamURL: "https://x/npo.m3u8"})
	if want := "npo1-a.m3u8/name npo1-b.m3u8/name"; urls(got) != want {
		t.Errorf("by name: %s\nwant %s", urls(got), want)
	}
	if got := tvCandidates(cat, store.Channel{Name: "Unknown", StreamURL: "https://x/u.m3u8"}); len(got) != 0 {
		t.Errorf("unknown channel: %s", urls(got))
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestFinderTV(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	id, _ := st.SaveChannel(ctx, store.Channel{Name: "RTV Noord", EPGID: "RTVNoord.nl@SD",
		StreamURL: "https://s/noord-dead.m3u8", Enabled: true})
	// Another channel already uses the HD stream.
	st.SaveChannel(ctx, store.Channel{Name: "Noord HD", StreamURL: "https://s/noord-hd.m3u8", Enabled: true})
	st.RecordHealth(ctx, id, false, "HTTP 404 Not Found", 0)

	p := &fakeProbe{plays: map[string]bool{"https://s/noord-sd.m3u8": true}}
	f := New(st, iptvClient(t), radioClient(t), p.probe, slog.New(slog.DiscardHandler))
	c, _ := st.Channel(ctx, id)
	n, err := f.Channel(ctx, c)
	if err != nil || n != 1 {
		t.Fatalf("found %d, %v", n, err)
	}
	if strings.Join(p.tested, " ") != "https://s/noord-sd.m3u8" {
		t.Errorf("tested %v", p.tested)
	}
	sugs, _ := st.Suggestions(ctx)
	if len(sugs[id]) != 1 || sugs[id][0].Detail != "HLS, segments load" || sugs[id][0].Source != store.SourceIPTVOrg {
		t.Errorf("suggestions %+v", sugs[id])
	}
}

func TestFinderRadio(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	id, _ := st.SaveChannel(ctx, store.Channel{Name: "Radio Noord", Kind: store.KindRadio, SourceID: "uuid-1",
		StreamURL: "https://r/noord-old.mp3", Enabled: true})
	st.RecordHealth(ctx, id, false, "HTTP 404 Not Found", 0)
	p := &fakeProbe{plays: map[string]bool{"https://r/noord-new.mp3": true, "https://r/noord-aac.aac": true,
		"https://r/oldies.mp3": true}}
	f := New(st, iptvClient(t), radioClient(t), p.probe, slog.New(slog.DiscardHandler))
	c, _ := st.Channel(ctx, id)
	if n, err := f.Channel(ctx, c); err != nil || n != 2 {
		t.Fatalf("found %d, %v", n, err)
	}
	sugs, _ := st.Suggestions(ctx)
	got := sugs[id]
	if len(got) != 2 || got[0].StreamURL != "https://r/noord-new.mp3" || got[0].Match != store.MatchSource ||
		got[0].Name != "Radio Noord (NL, MP3 128 kbit/s)" || got[1].SourceID != "uuid-2" || got[1].Match != store.MatchName {
		t.Errorf("suggestions %+v", got)
	}
}

func TestAuto(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	once, _ := st.SaveChannel(ctx, store.Channel{Name: "NPO 1", StreamURL: "https://x/a.m3u8", Enabled: true})
	twice, _ := st.SaveChannel(ctx, store.Channel{Name: "NPO 1", StreamURL: "https://x/b.m3u8", Enabled: true})
	st.RecordHealth(ctx, once, false, "HTTP 404 Not Found", 0)
	st.RecordHealth(ctx, twice, false, "HTTP 404 Not Found", 0)
	st.RecordHealth(ctx, twice, false, "HTTP 404 Not Found", 0)
	p := &fakeProbe{plays: map[string]bool{"https://s/npo1-a.m3u8": true}}
	f := New(st, iptvClient(t), radioClient(t), p.probe, slog.New(slog.DiscardHandler))

	f.Auto(ctx)
	sugs, _ := st.Suggestions(ctx)
	if len(sugs[once]) != 0 || len(sugs[twice]) != 1 {
		t.Fatalf("after first run: %+v", sugs)
	}
	if s := f.Status(); s.Running || s.Total != 1 || s.Found != 1 {
		t.Errorf("status %+v", s)
	}
	// A channel searched today is not searched again automatically.
	p.tested = nil
	f.Auto(ctx)
	if len(p.tested) != 0 {
		t.Errorf("searched again: %v", p.tested)
	}
	// Switched off: no search at all.
	hs, _ := st.HealthSettings(ctx)
	hs.Suggest = false
	st.SaveHealthSettings(ctx, hs)
	st.RecordHealth(ctx, once, false, "HTTP 404 Not Found", 0)
	f.Auto(ctx)
	if len(p.tested) != 0 {
		t.Errorf("searched while switched off: %v", p.tested)
	}
}
