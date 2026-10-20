package portal

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

type fixture struct {
	srv   *httptest.Server
	store *store.Store
	nh    int64 // channel in the user's package
	gld   int64 // channel outside it
	act   *activity.Tracker
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.ImportChannels(ctx, []store.ImportEntry{
		{Name: "NH", StreamURL: "https://s.example/nh/index.m3u8", EPGID: "nh.nl", LogoURL: "https://s.example/nh.png", Group: "North"},
		{Name: "Gld", StreamURL: "https://s.example/gld.m3u8", Group: "East"},
	}); err != nil {
		t.Fatal(err)
	}
	var f fixture
	cats, _ := st.Categories(ctx)
	var north int64
	for _, c := range cats {
		if c.Name == "North" {
			north = c.ID
		}
	}
	chs, _ := st.Channels(ctx, store.ChannelFilter{})
	for _, c := range chs {
		if c.Name == "NH" {
			f.nh = c.ID
		} else {
			f.gld = c.ID
		}
	}
	pkg, err := st.SavePackage(ctx, store.Package{Name: "North", CategoryIDs: []int64{north}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveUser(ctx, store.User{Username: "jan", Enabled: true, PackageIDs: []int64{pkg}}, "secret1"); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	ft := func(d time.Duration) string { return now.Add(d).Format("20060102150405 -0700") }
	guideSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<tv><channel id="nh.nl"><display-name>NH</display-name></channel>
<programme start="%s" stop="%s" channel="nh.nl"><title>Nieuws</title><desc>Vandaag</desc></programme>
<programme start="%s" stop="%s" channel="nh.nl"><title>Sport</title></programme></tv>`,
			ft(-10*time.Minute), ft(20*time.Minute), ft(20*time.Minute), ft(80*time.Minute))
	}))
	t.Cleanup(guideSrv.Close)
	guide := epg.New(slog.New(slog.DiscardHandler), t.TempDir())
	if err := guide.Refresh(ctx, []string{guideSrv.URL}); err != nil {
		t.Fatal(err)
	}

	f.act = activity.New()
	h, err := New(st, guide, f.act, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	f.store = st
	return f
}

func (f fixture) get(t *testing.T, path string) *http.Response {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f fixture) json(t *testing.T, path string, v any) {
	t.Helper()
	resp := f.get(t, path)
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

const creds = "username=jan&password=secret1"

func TestPlayerAPI(t *testing.T) {
	f := setup(t)

	var bad map[string]map[string]any
	f.json(t, "/player_api.php?username=jan&password=nope", &bad)
	if bad["user_info"]["auth"] != float64(0) {
		t.Fatalf("bad login = %v", bad)
	}

	var login struct {
		UserInfo   map[string]any `json:"user_info"`
		ServerInfo map[string]any `json:"server_info"`
	}
	f.json(t, "/player_api.php?"+creds, &login)
	if login.UserInfo["auth"] != float64(1) || login.UserInfo["status"] != "Active" || login.UserInfo["exp_date"] != nil {
		t.Fatalf("user_info = %v", login.UserInfo)
	}
	if login.ServerInfo["url"] != "127.0.0.1" || login.ServerInfo["server_protocol"] != "http" {
		t.Fatalf("server_info = %v", login.ServerInfo)
	}

	var cats []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_categories", &cats)
	if len(cats) != 1 || cats[0]["category_name"] != "North" {
		t.Fatalf("categories = %v", cats)
	}

	var streams []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_streams", &streams)
	if len(streams) != 1 || streams[0]["name"] != "NH" || streams[0]["epg_channel_id"] != "nh.nl" ||
		streams[0]["stream_id"] != float64(f.nh) || streams[0]["category_id"] != cats[0]["category_id"] {
		t.Fatalf("streams = %v", streams)
	}
	f.json(t, "/player_api.php?"+creds+"&action=get_live_streams&category_id=999", &streams)
	if len(streams) != 0 {
		t.Fatalf("filtered streams = %v", streams)
	}

	var vod []any
	f.json(t, "/player_api.php?"+creds+"&action=get_vod_streams", &vod)
	if len(vod) != 0 {
		t.Fatalf("vod = %v", vod)
	}

	var short struct {
		Listings []map[string]any `json:"epg_listings"`
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_short_epg&stream_id=%d&limit=1", creds, f.nh), &short)
	if len(short.Listings) != 1 {
		t.Fatalf("short epg = %v", short)
	}
	title, _ := base64.StdEncoding.DecodeString(short.Listings[0]["title"].(string))
	if string(title) != "Nieuws" {
		t.Fatalf("title = %q", title)
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_simple_data_table&stream_id=%d", creds, f.nh), &short)
	if len(short.Listings) != 2 || short.Listings[0]["now_playing"] != float64(1) {
		t.Fatalf("data table = %v", short)
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_short_epg&stream_id=%d", creds, f.gld), &short)
	if len(short.Listings) != 0 {
		t.Fatalf("epg of channel outside package = %v", short)
	}
}

func TestLiveRedirect(t *testing.T) {
	f := setup(t)
	resp := f.get(t, fmt.Sprintf("/live/jan/secret1/%d.ts", f.nh))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://s.example/nh/index.m3u8" {
		t.Fatalf("redirect = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := f.get(t, fmt.Sprintf("/live/jan/secret1/%d.m3u8", f.gld)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("channel outside package = %d", resp.StatusCode)
	}
	if resp := f.get(t, fmt.Sprintf("/live/jan/wrong1/%d.ts", f.nh)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", resp.StatusCode)
	}
	snap := f.act.Snapshot()
	if snap.OnlineUsers != 1 || snap.Watching != 1 || len(snap.Connections) != 1 ||
		snap.Connections[0].Username != "jan" || snap.Connections[0].Channel != "NH" {
		t.Fatalf("activity = %+v", snap)
	}

	// Disabling the user stops playback right away.
	u, _ := f.store.AuthenticateUser(context.Background(), "jan", "secret1")
	f.store.SetUserEnabled(context.Background(), u.ID, false)
	if resp := f.get(t, fmt.Sprintf("/live/jan/secret1/%d.ts", f.nh)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disabled user = %d", resp.StatusCode)
	}
	var login map[string]map[string]any
	f.json(t, "/player_api.php?"+creds, &login)
	if login["user_info"]["status"] != "Disabled" {
		t.Fatalf("disabled user_info = %v", login)
	}
}

func TestPlaylistAndGuide(t *testing.T) {
	f := setup(t)
	resp := f.get(t, "/get.php?"+creds+"&type=m3u_plus&output=ts")
	body, _ := io.ReadAll(resp.Body)
	pl := string(body)
	want := fmt.Sprintf("%s/live/jan/secret1/%d.ts", f.srv.URL, f.nh)
	if resp.StatusCode != 200 || !strings.HasPrefix(pl, `#EXTM3U url-tvg="`+f.srv.URL+"/xmltv.php?password=secret1&username=jan") ||
		!strings.Contains(pl, `tvg-id="nh.nl"`) || !strings.Contains(pl, `group-title="North",NH`) ||
		!strings.Contains(pl, want) || strings.Contains(pl, "Gld") {
		t.Fatalf("playlist (%d):\n%s", resp.StatusCode, pl)
	}
	if resp := f.get(t, "/get.php?username=jan&password=bad"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad playlist login = %d", resp.StatusCode)
	}

	resp = f.get(t, "/xmltv.php?"+creds)
	body, _ = io.ReadAll(resp.Body)
	x := string(body)
	if !strings.Contains(x, `<channel id="nh.nl"><display-name>NH</display-name><icon src="https://s.example/nh.png"/>`) ||
		strings.Count(x, "<programme") != 2 {
		t.Fatalf("xmltv:\n%s", x)
	}
}

func TestConnectionLimit(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	u.MaxConnections = 1
	if _, err := f.store.SaveUser(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	open := func(ip string) int {
		t.Helper()
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/live/jan/secret1/%d.ts", f.srv.URL, f.nh), nil)
		req.Header.Set("X-Forwarded-For", ip)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := open("198.51.100.1"); got != http.StatusFound {
		t.Fatalf("first place = %d", got)
	}
	if got := open("198.51.100.1"); got != http.StatusFound {
		t.Fatalf("switching channel at the same place = %d", got)
	}
	if got := open("198.51.100.2"); got != http.StatusForbidden {
		t.Fatalf("second place = %d, want 403", got)
	}

	var login map[string]map[string]any
	f.json(t, "/player_api.php?"+creds, &login)
	if login["user_info"]["max_connections"] != "1" || login["user_info"]["active_cons"] != "1" {
		t.Fatalf("user_info = %v", login["user_info"])
	}

	f.act.Release(u.ID)
	if got := open("198.51.100.2"); got != http.StatusFound {
		t.Fatalf("after release = %d", got)
	}
}
