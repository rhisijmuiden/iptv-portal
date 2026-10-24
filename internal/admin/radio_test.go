package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// fakeRadioBrowser answers like the Radio Browser API.
func fakeRadioBrowser(t *testing.T) *httptest.Server {
	t.Helper()
	byID := map[string]string{
		"u1": `{"stationuuid":"u1","name":"NPO Radio 1","url_resolved":"https://icecast.example/radio1.mp3","favicon":"https://x.example/r1.png",
		 "tags":"news,talk","country":"The Netherlands","countrycode":"NL","language":"dutch","codec":"MP3","bitrate":192,"votes":900,"lastcheckok":1}`,
		"u2": `{"stationuuid":"u2","name":"Pop FM","url_resolved":"https://icecast.example/pop.aac","favicon":"data:image/png;base64,xx",
		 "tags":"top 40,pop","country":"The Netherlands","countrycode":"NL","language":"dutch,english","codec":"AAC","votes":50,"lastcheckok":1}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/stations/search":
			w.Write([]byte("[" + byID["u1"] + "," + byID["u2"] + "]"))
		case "/json/stations/byuuid":
			var keep []string
			for _, id := range strings.Split(r.FormValue("uuids"), ",") {
				if s, ok := byID[id]; ok {
					keep = append(keep, s)
				}
			}
			w.Write([]byte("[" + strings.Join(keep, ",") + "]"))
		case "/json/countries":
			w.Write([]byte(`[{"name":"The Netherlands","iso_3166_1":"NL","stationcount":800}]`))
		case "/json/languages":
			w.Write([]byte(`[{"name":"dutch","iso_639":"nl","stationcount":700}]`))
		case "/json/tags":
			w.Write([]byte(`[{"name":"pop","stationcount":9000}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRadioBrowse(t *testing.T) {
	h, st := newTestHandler(t, "")
	h.radio.BaseURL = fakeRadioBrowser(t).URL
	req := loggedIn(t, h, st)
	ctx := context.Background()
	pkg, err := st.SavePackage(ctx, store.Package{Name: "Radio", OnDemand: true})
	if err != nil {
		t.Fatal(err)
	}

	body := req("GET", "/radio/browse?tag=pop", nil).Body.String()
	for _, want := range []string{"NPO Radio 1", "Pop FM", `value="u1"`, "The Netherlands (800)", "192 kbit/s",
		"Use the genre (Radio Pop)", `name="filter_tag" value="pop"`} {
		if !strings.Contains(body, want) {
			t.Errorf("browse page is missing %q", want)
		}
	}

	w := req("POST", "/radio/browse/add", url.Values{"station": {"u1", "u2"}, "target": {"@tag"}, "filter_tag": {"pop"},
		"package": {itoa(pkg)}, "back": {"/radio/browse?tag=pop"}})
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "2+radio+stations+added") {
		t.Fatalf("add: %d %q", w.Code, w.Header().Get("Location"))
	}
	radios, _ := st.Channels(ctx, store.ChannelFilter{Kind: store.KindRadio})
	if len(radios) != 2 {
		t.Fatalf("%d radio stations, want 2", len(radios))
	}
	got := map[string]store.Channel{}
	for _, c := range radios {
		got[c.SourceID] = c
	}
	if c := got["u1"]; c.Category != "Radio News" || c.LogoURL != "https://x.example/r1.png" || c.StreamURL != "https://icecast.example/radio1.mp3" {
		t.Errorf("u1 = %+v", c)
	}
	if c := got["u2"]; c.Category != "Radio Pop" || c.LogoURL != "" {
		t.Errorf("u2 = %+v (the searched tag names the category, a data: logo is dropped)", c)
	}
	if tv, _ := st.Channels(ctx, store.ChannelFilter{Kind: store.KindTV}); len(tv) != 0 {
		t.Errorf("stations ended up as TV channels: %+v", tv)
	}
	p, _ := st.Package(ctx, pkg)
	if len(p.CategoryIDs) != 2 {
		t.Errorf("package categories = %v, want both radio categories", p.CategoryIDs)
	}

	// Adding again updates instead of duplicating, and the page shows them
	// as in the portal.
	w = req("POST", "/radio/browse/add", url.Values{"station": {"u1"}, "target": {""}})
	if !strings.Contains(w.Header().Get("Location"), "0+radio+stations+added%2C+1+updated") {
		t.Errorf("add again: %q", w.Header().Get("Location"))
	}
	if body := req("GET", "/radio/browse", nil).Body.String(); strings.Count(body, ">in portal</span>") != 2 {
		t.Error("browse page does not mark the added stations")
	}

	body = req("GET", "/radio", nil).Body.String()
	for _, want := range []string{"NPO Radio 1", "Radio News", "Imported from Radio Browser"} {
		if !strings.Contains(body, want) {
			t.Errorf("radio list is missing %q", want)
		}
	}
	if strings.Contains(req("GET", "/channels", nil).Body.String(), "NPO Radio 1") {
		t.Error("radio station listed among the TV channels")
	}
}

func TestRadioBrowseUnreachable(t *testing.T) {
	h, st := newTestHandler(t, "")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	h.radio.BaseURL = down.URL
	req := loggedIn(t, h, st)
	if body := req("GET", "/radio/browse", nil).Body.String(); !strings.Contains(body, "Could not reach Radio Browser") {
		t.Error("no error shown when Radio Browser is down")
	}
}

func TestRadioManual(t *testing.T) {
	pls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[playlist]\nFile1=https://stream.example/live.mp3\n"))
	}))
	defer pls.Close()
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()

	if body := req("GET", "/radio/new", nil).Body.String(); !strings.Contains(body, "Add radio station") ||
		!strings.Contains(body, `action="/radio/new"`) {
		t.Fatal("new station form not shown")
	}
	w := req("POST", "/radio/new", url.Values{"name": {"Local FM"}, "stream_url": {pls.URL + "/listen.pls"},
		"new_category": {"Radio Local"}, "enabled": {"on"}})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") == "" || !strings.HasPrefix(w.Header().Get("Location"), "/radio?") {
		t.Fatalf("save: %d %q %s", w.Code, w.Header().Get("Location"), w.Body)
	}
	radios, _ := st.Channels(ctx, store.ChannelFilter{Kind: store.KindRadio})
	if len(radios) != 1 || radios[0].StreamURL != "https://stream.example/live.mp3" || radios[0].Category != "Radio Local" {
		t.Fatalf("saved %+v", radios)
	}
	id := radios[0].ID
	cats, _ := st.CategoriesOf(ctx, store.KindRadio)
	if len(cats) != 1 || cats[0].Name != "Radio Local" {
		t.Errorf("radio categories = %+v", cats)
	}

	// A radio station opened as a TV channel goes to the radio form, and
	// the TV form can't change it.
	if w := req("GET", "/channels/"+itoa(id), nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/radio/"+itoa(id) {
		t.Errorf("open as channel: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := req("POST", "/channels/"+itoa(id), url.Values{"name": {"X"}, "stream_url": {"https://x.example/a.m3u8"}}); w.Code != http.StatusNotFound {
		t.Errorf("save as channel: %d", w.Code)
	}
	if body := req("GET", "/radio/"+itoa(id), nil).Body.String(); !strings.Contains(body, "Edit radio station") || !strings.Contains(body, "Local FM") {
		t.Error("edit form not shown")
	}

	if w := req("POST", "/radio/"+itoa(id)+"/delete", url.Values{}); w.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", w.Code)
	}
	if radios, _ := st.Channels(ctx, store.ChannelFilter{Kind: store.KindRadio}); len(radios) != 0 {
		t.Error("station not deleted")
	}
}

func TestCategoryKinds(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	req("POST", "/categories", url.Values{"name": {"News"}, "kind": {"tv"}})
	req("POST", "/categories", url.Values{"name": {"Radio Jazz"}, "kind": {"radio"}})
	tv, _ := st.CategoriesOf(ctx, store.KindTV)
	radio, _ := st.CategoriesOf(ctx, store.KindRadio)
	if len(tv) != 1 || tv[0].Name != "News" || len(radio) != 1 || radio[0].Name != "Radio Jazz" {
		t.Fatalf("tv %+v, radio %+v", tv, radio)
	}
	body := req("GET", "/categories", nil).Body.String()
	if !strings.Contains(body, `href="/radio?category=`+itoa(radio[0].ID)+`"`) {
		t.Error("radio category does not link to the radio list")
	}
	if body := req("GET", "/channels/new", nil).Body.String(); strings.Contains(body, "Radio Jazz") {
		t.Error("TV channel form offers a radio category")
	}
}
