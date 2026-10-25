package portal

import (
	"context"
	"encoding/base64"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// addRadio adds a station whose stream sends ICY metadata to the user's
// package and returns its id.
func addRadio(t *testing.T, f fixture) int64 {
	t.Helper()
	ctx := context.Background()
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("icy-metaint", "4096")
		w.Header().Set("icy-genre", "Hits")
		w.Write(make([]byte, 4096))
		meta := "StreamTitle='Marco Borsato - Rood';"
		block := make([]byte, 48)
		copy(block, meta)
		w.Write(append([]byte{3}, block...))
	}))
	t.Cleanup(stream.Close)
	if _, err := f.store.ImportChannels(ctx, []store.ImportEntry{
		{Kind: store.KindRadio, Name: "Radio Noord", StreamURL: stream.URL + "/live.mp3", Group: "Regional"},
	}); err != nil {
		t.Fatal(err)
	}
	pkgs, _ := f.store.Packages(ctx)
	if err := f.store.AddCategoriesToPackages(ctx, []int64{pkgs[0].ID}, store.KindRadio, []string{"Regional"}); err != nil {
		t.Fatal(err)
	}
	chs, _ := f.store.Channels(ctx, store.ChannelFilter{Kind: store.KindRadio})
	return chs[0].ID
}

func TestRadioGuide(t *testing.T) {
	f := setup(t)
	id := addRadio(t, f)
	epgID := fmt.Sprintf("radio%d.iptv-portal", id)

	var streams []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_streams", &streams)
	var radio map[string]any
	for _, s := range streams {
		if s["name"] == "Radio Noord" {
			radio = s
		}
	}
	if radio == nil || radio["epg_channel_id"] != epgID ||
		radio["stream_icon"] != fmt.Sprintf("%s/logo/%d.png", f.srv.URL, id) {
		t.Fatalf("radio stream = %v", radio)
	}

	var short struct {
		Listings []map[string]any `json:"epg_listings"`
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_short_epg&stream_id=%d", creds, id), &short)
	if len(short.Listings) != 4 {
		t.Fatalf("short epg = %v", short)
	}
	title, _ := base64.StdEncoding.DecodeString(short.Listings[0]["title"].(string))
	desc, _ := base64.StdEncoding.DecodeString(short.Listings[0]["description"].(string))
	next, _ := base64.StdEncoding.DecodeString(short.Listings[1]["title"].(string))
	if string(title) != "Marco Borsato - Rood" || !strings.Contains(string(desc), "Rood by Marco Borsato") ||
		string(next) != "Radio Noord" || short.Listings[0]["epg_id"] != epgID {
		t.Fatalf("listings: %q / %q / %q", title, desc, next)
	}

	// The XMLTV guide has the station, its logo and the cached song.
	body, _ := io.ReadAll(f.get(t, "/xmltv.php?"+creds).Body)
	guide := string(body)
	for _, want := range []string{
		`<channel id="` + epgID + `"><display-name>Radio Noord</display-name><icon src="` + f.srv.URL + fmt.Sprintf("/logo/%d.png", id),
		`channel="` + epgID + `"><title>Marco Borsato - Rood</title>`,
		`<title>Radio Noord</title><desc>Live radio from Radio Noord. Genre: Hits.</desc>`,
	} {
		if !strings.Contains(guide, want) {
			t.Fatalf("guide lacks %q:\n%s", want, guide)
		}
	}

	body, _ = io.ReadAll(f.get(t, "/get.php?"+creds+"&type=m3u_plus").Body)
	if !strings.Contains(string(body), `tvg-id="`+epgID+`"`) || !strings.Contains(string(body), fmt.Sprintf(`/logo/%d.png"`, id)) {
		t.Fatalf("playlist:\n%s", body)
	}

	// Switched off, the guide only has the station's name.
	st, _ := f.store.Settings(context.Background())
	st.RadioNowPlaying = false
	if err := f.store.SaveSettings(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_short_epg&stream_id=%d", creds, id), &short)
	title, _ = base64.StdEncoding.DecodeString(short.Listings[0]["title"].(string))
	if string(title) != "Radio Noord" {
		t.Fatalf("title with now playing off = %q", title)
	}
}

func TestRadioLogo(t *testing.T) {
	f := setup(t)
	id := addRadio(t, f)
	resp := f.get(t, fmt.Sprintf("/logo/%d.png", id))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("logo: %s %s", resp.Status, resp.Header.Get("Content-Type"))
	}
	if _, err := png.Decode(resp.Body); err != nil {
		t.Fatal(err)
	}
	// TV channels and unknown ids have no portal logo.
	for _, p := range []string{fmt.Sprintf("/logo/%d.png", f.nh), "/logo/999.png", "/logo/x.png"} {
		if resp := f.get(t, p); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %s", p, resp.Status)
		}
	}
	if err := f.store.SetChannelEnabled(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	if resp := f.get(t, fmt.Sprintf("/logo/%d.png", id)); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled station logo: %s", resp.Status)
	}
}
