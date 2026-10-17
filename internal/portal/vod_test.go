package portal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

var movieData = bytes.Repeat([]byte("0123456789"), 100)

// vodSetup starts a fake Jellyfin server and a portal offering it. User
// "piet" has On Demand, user "jan" does not.
func vodSetup(t *testing.T, direct bool) (fixture, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "jf-key" && !strings.Contains(r.Header.Get("Authorization"), `Token="jf-key"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Cookie") != "" {
			t.Errorf("cookie passed on to the media server")
		}
		switch {
		case r.URL.Path == "/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Name":"Films","ItemId":"lm","CollectionType":"movies"},{"Name":"Series","ItemId":"ls","CollectionType":"tvshows"}]`)
		case r.URL.Path == "/Users":
			fmt.Fprint(w, `[{"Id":"admin","Policy":{"IsAdministrator":true}}]`)
		case r.URL.Path == "/Items":
			switch r.URL.Query().Get("IncludeItemTypes") {
			case "Movie":
				fmt.Fprint(w, `{"Items":[{"Id":"m1","Name":"Heat","ProductionYear":1995,"CommunityRating":8.3,
					"RunTimeTicks":36000000000,"Path":"/heat.mkv","ImageTags":{"Primary":"a"},"Genres":["Crime","Drama"]}]}`)
			case "Series":
				fmt.Fprint(w, `{"Items":[{"Id":"s1","Name":"Show","ImageTags":{"Primary":"a"}}]}`)
			case "Episode":
				fmt.Fprint(w, `{"Items":[{"Id":"e1","Name":"Pilot","SeriesId":"s1","ParentIndexNumber":1,"IndexNumber":1,"Path":"/p.mp4"},
					{"Id":"e2","Name":"Next","SeriesId":"s1","ParentIndexNumber":1,"IndexNumber":2,"Path":"/n.mp4"},
					{"Id":"e3","Name":"Return","SeriesId":"s1","ParentIndexNumber":2,"IndexNumber":1,"Path":"/r.mp4"}]}`)
			}
		case r.URL.Path == "/Videos/m1/stream":
			w.Header().Set("Set-Cookie", "secret=1")
			http.ServeContent(w, r, "heat.mkv", time.Time{}, bytes.NewReader(movieData))
		case r.URL.Path == "/Videos/e3/stream":
			w.Write([]byte("episode"))
		case r.URL.Path == "/Items/m1/Images/Primary":
			w.Write([]byte("poster"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(jf.Close)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.SaveMediaServer(ctx, store.MediaServer{Name: "Home", Kind: store.MediaJellyfin, URL: jf.URL,
		Token: "jf-key", Libraries: []string{"lm", "ls"}, Direct: direct, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	pkg, err := st.SavePackage(ctx, store.Package{Name: "Films", OnDemand: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveUser(ctx, store.User{Username: "piet", Enabled: true, PackageIDs: []int64{pkg}}, "secret2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true}, "secret1"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	vod := media.NewCatalog(st, log)
	vod.Refresh(ctx)
	act := activity.New()
	h, err := New(st, epg.New(log, t.TempDir()), act, vod, log)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{srv: httptest.NewServer(h), store: st, act: act}
	t.Cleanup(f.srv.Close)
	return f, jf
}

const vodCreds = "username=piet&password=secret2"

func TestVODAPI(t *testing.T) {
	f, _ := vodSetup(t, false)

	var cats []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_categories", &cats)
	if len(cats) != 1 || cats[0]["category_name"] != "Films" {
		t.Fatalf("vod categories = %v", cats)
	}
	var movies []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_streams&category_id="+cats[0]["category_id"].(string), &movies)
	if len(movies) != 1 || movies[0]["name"] != "Heat" || movies[0]["container_extension"] != "mkv" ||
		movies[0]["rating"] != "8.3" || movies[0]["stream_type"] != "movie" {
		t.Fatalf("vod streams = %v", movies)
	}
	icon := movies[0]["stream_icon"].(string)
	if !strings.HasPrefix(icon, f.srv.URL+"/vod-image/") {
		t.Errorf("stream_icon = %s", icon)
	}
	id := int64(movies[0]["stream_id"].(float64))

	var info struct {
		Info      map[string]any `json:"info"`
		MovieData map[string]any `json:"movie_data"`
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_vod_info&vod_id=%d", vodCreds, id), &info)
	if info.Info["genre"] != "Crime, Drama" || info.Info["duration"] != "01:00:00" || info.MovieData["name"] != "Heat" {
		t.Fatalf("vod info = %+v", info)
	}

	var series []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_series", &series)
	if len(series) != 1 || series[0]["name"] != "Show" {
		t.Fatalf("series = %v", series)
	}
	var sinfo struct {
		Seasons  []map[string]any            `json:"seasons"`
		Episodes map[string][]map[string]any `json:"episodes"`
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_series_info&series_id=%v", vodCreds, series[0]["series_id"]), &sinfo)
	if len(sinfo.Seasons) != 2 || sinfo.Seasons[0]["episode_count"] != float64(2) ||
		len(sinfo.Episodes["1"]) != 2 || sinfo.Episodes["2"][0]["title"] != "Return" {
		t.Fatalf("series info = %+v", sinfo)
	}

	// Without On Demand in a package nothing is listed.
	var none []any
	f.json(t, "/player_api.php?"+creds+"&action=get_vod_streams", &none)
	if len(none) != 0 {
		t.Errorf("user without On Demand sees %v", none)
	}
	resp := f.get(t, fmt.Sprintf("/movie/jan/secret1/%d.mkv", id))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("user without On Demand: playback status %d", resp.StatusCode)
	}

	// Images need no login and never reveal the token.
	resp = f.get(t, strings.TrimPrefix(icon, f.srv.URL))
	if b, _ := io.ReadAll(resp.Body); string(b) != "poster" {
		t.Errorf("poster = %q", b)
	}
	if resp := f.get(t, "/vod-image/999999/poster"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown image: %d", resp.StatusCode)
	}

	// Episode playback.
	ep := sinfo.Episodes["2"][0]
	resp = f.get(t, fmt.Sprintf("/series/piet/secret2/%s.%s", ep["id"], ep["container_extension"]))
	if b, _ := io.ReadAll(resp.Body); string(b) != "episode" {
		t.Errorf("episode = %q", b)
	}
	if c := f.act.Snapshot().Connections; len(c) == 0 || c[0].Channel != "Show S02E01" {
		t.Errorf("dashboard activity = %+v", c)
	}
	// A movie id is not an episode.
	if resp := f.get(t, fmt.Sprintf("/series/piet/secret2/%d.mkv", id)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("movie played as episode: %d", resp.StatusCode)
	}
}

func TestVODProxy(t *testing.T) {
	f, _ := vodSetup(t, false)
	var movies []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_streams", &movies)
	url := fmt.Sprintf("%s/movie/piet/secret2/%v.mkv", f.srv.URL, movies[0]["stream_id"])

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Range", "bytes=10-19")
	req.Header.Set("Cookie", "portal=1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(b) != "0123456789" {
		t.Fatalf("range request: %d %q", resp.StatusCode, b)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Error("media server cookie passed to the player")
	}

	resp2, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if b, _ := io.ReadAll(resp2.Body); !bytes.Equal(b, movieData) {
		t.Errorf("full movie: got %d bytes", len(b))
	}
}

func TestVODDirect(t *testing.T) {
	f, jf := vodSetup(t, true)
	var movies []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_streams", &movies)
	resp := f.get(t, fmt.Sprintf("/movie/piet/secret2/%v.mkv", movies[0]["stream_id"]))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != jf.URL+"/Videos/m1/stream?static=true&api_key=jf-key" {
		t.Fatalf("direct playback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}
