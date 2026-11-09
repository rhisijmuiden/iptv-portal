package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// fetch does a request with the browser's cookies and returns the status
// and body.
func (b *browser) fetch(method, path string, form url.Values, header http.Header) (int, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, b.base+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func (b *browser) play(csrf, kind string, id int64, header http.Header) (int, playInfo, string) {
	b.t.Helper()
	code, body := b.fetch("POST", "/account/watch/play", url.Values{"csrf": {csrf}, "kind": {kind}, "id": {strconv.FormatInt(id, 10)}}, header)
	var p playInfo
	json.Unmarshal([]byte(body), &p)
	return code, p, body
}

// hlsSource is a broadcaster serving an HLS stream with a master
// playlist, a media playlist with an encryption key and segments.
func hlsSource(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nhd/chunks.m3u8?q=1\n")
		case "/live/hd/chunks.m3u8":
			w.Header().Set("Content-Type", "text/plain") // some sources say so
			fmt.Fprint(w, "#EXTM3U\r\n#EXT-X-TARGETDURATION:6\r\n#EXT-X-KEY:METHOD=AES-128,URI=\"/keys/k1\"\r\n#EXTINF:6.0,\r\nseg1.ts\r\n#EXTINF:6.0,\r\nhttps://cdn.example/seg2.ts\r\n")
		case "/live/hd/seg1.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write([]byte("segment-one"))
		case "/keys/k1":
			w.Write([]byte("0123456789abcdef"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWatchLive(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	src := hlsSource(t)
	c, err := f.store.Channel(ctx, f.nh)
	if err != nil {
		t.Fatal(err)
	}
	c.StreamURL = src.URL + "/live/index.m3u8"
	if _, err := f.store.SaveChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	b := login(t, f, "jan", "secret1")

	_, _, body := b.do("GET", "/account/watch", nil)
	for _, want := range []string{`href="/account/watch" class="active"`, `data-name="NH"`, `/static/vendor/hls.min.js`, `"csrf":`} {
		if !strings.Contains(body, want) {
			t.Errorf("watch page lacks %q", want)
		}
	}
	if strings.Contains(body, `data-name="Gld"`) {
		t.Error("watch page shows a channel outside the package")
	}
	csrf := csrfRE.FindStringSubmatch(body)
	if csrf == nil {
		t.Fatal("no CSRF token on the watch page")
	}
	token := csrf[1]

	// Auto: an http stream on an http page plays from the source, with
	// the portal as fallback.
	code, p, raw := b.play(token, "live", f.nh, nil)
	if code != http.StatusOK || p.Type != "hls" || p.URL != c.StreamURL || !strings.HasPrefix(p.Fallback, "/account/stream/") ||
		p.Now != "Nieuws" || p.Next != "Sport" {
		t.Fatalf("play = %d %s", code, raw)
	}
	if f.act.ActiveConnections(mustUser(t, f, "jan").ID) != 1 {
		t.Error("playing does not count as a connection")
	}
	if code, _, _ := b.play(token, "live", f.gld, nil); code != http.StatusNotFound {
		t.Errorf("channel outside the package: %d", code)
	}
	if code, _, _ := b.play("wrong", "live", f.nh, nil); code != http.StatusForbidden {
		t.Errorf("wrong CSRF token: %d", code)
	}

	// The playlists through the portal point to the portal only.
	code, master := b.fetch("GET", p.Fallback, nil, nil)
	if code != http.StatusOK || !strings.HasPrefix(master, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\n/account/stream/") {
		t.Fatalf("master playlist = %d\n%s", code, master)
	}
	variant := strings.Split(master, "\n")[2]
	code, media := b.fetch("GET", variant, nil, nil)
	if code != http.StatusOK || strings.Contains(media, src.URL) || strings.Contains(media, "cdn.example") ||
		!strings.Contains(media, `URI="/account/stream/`) {
		t.Fatalf("media playlist = %d\n%s", code, media)
	}
	var seg, key string
	for _, l := range strings.Split(media, "\n") {
		if strings.HasPrefix(l, "/account/stream/") && seg == "" {
			seg = l
		}
		if i := strings.Index(l, `URI="`); i >= 0 {
			key = strings.TrimSuffix(l[i+5:], `"`)
		}
	}
	if code, data := b.fetch("GET", seg, nil, nil); code != http.StatusOK || data != "segment-one" {
		t.Errorf("segment = %d %q", code, data)
	}
	if code, data := b.fetch("GET", key, nil, nil); code != http.StatusOK || data != "0123456789abcdef" {
		t.Errorf("key = %d %q", code, data)
	}

	// A changed address does not work, and neither does another user's.
	if code, _ := b.fetch("GET", seg[:len(seg)-3]+"abc", nil, nil); code != http.StatusNotFound {
		t.Errorf("changed address: %d", code)
	}
	if _, err := f.store.SaveUser(ctx, store.User{Username: "els", Enabled: true}, "secret3"); err != nil {
		t.Fatal(err)
	}
	other := login(t, f, "els", "secret3")
	if code, _ := other.fetch("GET", seg, nil, nil); code != http.StatusNotFound {
		t.Errorf("another user's address: %d", code)
	}
	if code, _ := newBrowser(t, f).fetch("GET", seg, nil, nil); code != http.StatusSeeOther {
		t.Errorf("address without login: %d", code)
	}

	// An https page gets http streams through the portal.
	_, p, _ = b.play(token, "live", f.nh, http.Header{"X-Forwarded-Proto": {"https"}})
	if !strings.HasPrefix(p.URL, "/account/stream/") || p.Fallback != "" {
		t.Errorf("mixed content: %+v", p)
	}
	st, _ := f.store.Settings(ctx)
	st.WebPlayerProxy = store.WebPlayerAlways
	f.store.SaveSettings(ctx, st)
	if _, p, _ = b.play(token, "live", f.nh, nil); !strings.HasPrefix(p.URL, "/account/stream/") || p.Fallback != "" {
		t.Errorf("always through the portal: %+v", p)
	}
	st.WebPlayerProxy = store.WebPlayerNever
	f.store.SaveSettings(ctx, st)
	if _, p, _ = b.play(token, "live", f.nh, http.Header{"X-Forwarded-Proto": {"https"}}); p.URL != c.StreamURL || p.Fallback != "" {
		t.Errorf("never through the portal: %+v", p)
	}
}

func mustUser(t *testing.T, f fixture, name string) store.User {
	t.Helper()
	users, err := f.store.Users(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == name {
			return u
		}
	}
	t.Fatalf("no user %s", name)
	return store.User{}
}

func TestWatchLimitsAndSwitch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := mustUser(t, f, "jan")
	u.MaxConnections = 1
	if _, err := f.store.SaveUser(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	b := login(t, f, "jan", "secret1")
	token := b.csrf("/account/watch")

	// The TV box at another address already watches.
	f.act.Open(u.ID, "jan", "192.0.2.7", "MyTVOnline", f.nh, "NH", 0)
	if code, _, raw := b.play(token, "live", f.nh, nil); code != http.StatusForbidden || !strings.Contains(raw, `"limit"`) {
		t.Errorf("over the limit: %d %s", code, raw)
	}

	u.Enabled = false
	f.store.SaveUser(ctx, u, "")
	if code, _, raw := b.play(token, "live", f.nh, nil); code != http.StatusForbidden || !strings.Contains(raw, `"inactive"`) {
		t.Errorf("disabled account: %d %s", code, raw)
	}

	st, _ := f.store.Settings(ctx)
	st.WebPlayer = false
	f.store.SaveSettings(ctx, st)
	if code, _ := b.fetch("GET", "/account/watch", nil, nil); code != http.StatusNotFound {
		t.Errorf("web player off: %d", code)
	}
	if _, _, body := b.do("GET", "/account", nil); strings.Contains(body, "/account/watch") {
		t.Error("menu links to the web player while it is off")
	}
}

func TestWatchOnDemand(t *testing.T) {
	f, _ := vodSetup(t, true) // direct playback for players; the browser still goes through the portal
	b := login(t, f, "piet", "secret2")
	token := b.csrf("/account/watch")

	var lib struct {
		Categories []watchCategory `json:"categories"`
		Items      []watchItem     `json:"items"`
	}
	code, body := b.fetch("GET", "/account/watch/library?type=movies", nil, nil)
	if code != http.StatusOK || json.Unmarshal([]byte(body), &lib) != nil || len(lib.Items) != 1 || lib.Items[0].Title != "Heat" ||
		len(lib.Categories) != 2 || !strings.HasPrefix(lib.Items[0].Poster, "/vod-image/") {
		t.Fatalf("movies = %d %s", code, body)
	}

	// Heat is an mkv: Jellyfin makes HLS of it.
	code, p, raw := b.play(token, "movie", lib.Items[0].ID, nil)
	if code != http.StatusOK || p.Type != "hls" || !strings.HasPrefix(p.URL, "/account/stream/") || p.Title != "Heat" {
		t.Fatalf("play movie = %d %s", code, raw)
	}
	if strings.Contains(raw, "jf-key") {
		t.Error("media server token handed to the browser")
	}
	code, master := b.fetch("GET", p.URL, nil, nil)
	if code != http.StatusOK || !strings.Contains(master, "/account/stream/") {
		t.Fatalf("movie playlist = %d\n%s", code, master)
	}
	var next string
	for _, l := range strings.Split(master, "\n") {
		if strings.HasPrefix(l, "/account/stream/") {
			next = l
		}
	}
	// The relative address in Jellyfin's playlist gets the token again.
	if code, data := b.fetch("GET", next, nil, nil); code != http.StatusOK || data != "heat-segment" {
		t.Errorf("movie segment = %d %q", code, data)
	}

	code, body = b.fetch("GET", "/account/watch/library?type=series", nil, nil)
	if code != http.StatusOK || json.Unmarshal([]byte(body), &lib) != nil || len(lib.Items) != 1 {
		t.Fatalf("series = %d %s", code, body)
	}
	var show struct {
		Seasons []watchSeason `json:"seasons"`
	}
	code, body = b.fetch("GET", fmt.Sprintf("/account/watch/series/%d", lib.Items[0].ID), nil, nil)
	if code != http.StatusOK || json.Unmarshal([]byte(body), &show) != nil || len(show.Seasons) != 2 ||
		len(show.Seasons[0].Episodes) != 2 || show.Seasons[1].Number != 2 {
		t.Fatalf("series detail = %d %s", code, body)
	}

	// An mp4 episode plays as it is, with seeking.
	ep := show.Seasons[1].Episodes[0]
	code, p, raw = b.play(token, "episode", ep.ID, nil)
	if code != http.StatusOK || p.Type != "video" || p.Title != "Show S02E01" {
		t.Fatalf("play episode = %d %s", code, raw)
	}
	if code, data := b.fetch("GET", p.URL, nil, nil); code != http.StatusOK || data != "episode" {
		t.Errorf("episode = %d %q", code, data)
	}

	// Without On Demand in the package there is nothing to list or play.
	jan := login(t, f, "jan", "secret1")
	jt := jan.csrf("/account/watch")
	if code, _ := jan.fetch("GET", "/account/watch/library?type=movies", nil, nil); code != http.StatusNotFound {
		t.Errorf("library without On Demand: %d", code)
	}
	if code, _, _ := jan.play(jt, "movie", lib.Items[0].ID, nil); code != http.StatusNotFound {
		t.Errorf("movie without On Demand: %d", code)
	}
}

func TestRewritePlaylist(t *testing.T) {
	base, _ := url.Parse("http://src.example/a/b/index.m3u8?t=1")
	in := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://key\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"../audio/en.m3u8\"\n#EXTINF:4,\n  part1.ts  \n\n#EXT-X-ENDLIST\n"
	out := string(rewritePlaylist([]byte(in), base, func(u *url.URL) string { return "[" + u.String() + "]" }))
	want := "#EXTM3U\n#EXT-X-MAP:URI=\"[http://src.example/a/b/init.mp4]\"\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://key\"\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"[http://src.example/a/audio/en.m3u8]\"\n#EXTINF:4,\n[http://src.example/a/b/part1.ts]\n\n#EXT-X-ENDLIST\n"
	if out != want {
		t.Errorf("rewritten:\n%s\nwant:\n%s", out, want)
	}
}

func TestStreamFormat(t *testing.T) {
	for _, tc := range []struct {
		url, kind, want string
	}{
		{"https://s.example/live/index.m3u8", store.KindTV, "hls"},
		{"https://s.example/live/stream", store.KindTV, "hls"},
		{"https://s.example/live/stream.ts", store.KindTV, "unsupported"},
		{"rtmp://s.example/live", store.KindTV, "unsupported"},
		{"https://s.example/movie.mp4", store.KindTV, "video"},
		{"https://icecast.example/radio.mp3", store.KindRadio, "audio"},
		{"https://icecast.example/stream", store.KindRadio, "audio"},
		{"https://s.example/radio/playlist.m3u8", store.KindRadio, "hls"},
	} {
		if got := streamFormat(store.Channel{StreamURL: tc.url, Kind: tc.kind}); got != tc.want {
			t.Errorf("streamFormat(%s, %s) = %s, want %s", tc.url, tc.kind, got, tc.want)
		}
	}
}

func TestWatchParentalLock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := mustUser(t, f, "jan")
	c, _ := f.store.Channel(ctx, f.nh)
	if err := f.store.SetParentalPIN(ctx, u.ID, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetCategoryLocked(ctx, u.ID, c.CategoryID, true); err != nil {
		t.Fatal(err)
	}
	b := login(t, f, "jan", "secret1")
	_, _, body := b.do("GET", "/account/watch", nil)
	if strings.Contains(body, `data-name="NH"`) {
		t.Error("watch page shows a locked channel")
	}
	token := csrfRE.FindStringSubmatch(body)[1]
	if code, _, _ := b.play(token, "live", f.nh, nil); code != http.StatusNotFound {
		t.Errorf("locked channel plays: %d", code)
	}
}
