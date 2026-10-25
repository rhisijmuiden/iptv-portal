package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestRadioNowPlaying(t *testing.T) {
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/quiet.mp3" {
			w.Write(make([]byte, 100))
			return
		}
		w.Header().Set("icy-metaint", "16")
		block := make([]byte, 32)
		copy(block, "StreamTitle='Queen - Radio Ga Ga';")
		w.Write(append(append(make([]byte, 16), 2), block...))
	}))
	defer stream.Close()
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	if _, err := st.ImportChannels(ctx, []store.ImportEntry{
		{Kind: store.KindRadio, Name: "Hits FM", StreamURL: stream.URL + "/hits.mp3"},
		{Kind: store.KindRadio, Name: "Quiet FM", StreamURL: stream.URL + "/quiet.mp3"},
	}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	chs, _ := st.Channels(ctx, store.ChannelFilter{Kind: store.KindRadio})
	for _, c := range chs {
		ids[c.Name] = itoa(c.ID)
	}

	body := req("GET", "/radio", nil).Body.String()
	for _, want := range []string{`class="now-playing" data-id="` + ids["Hits FM"] + `"`, "/radio/" + ids["Hits FM"] + "/logo.png",
		`name="on" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("radio page is missing %q", want)
		}
	}

	w := req("GET", "/radio/now-playing?id="+ids["Hits FM"]+"&id="+ids["Quiet FM"], nil)
	var got map[string]nowPlayingJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[ids["Hits FM"]].Title != "Queen - Radio Ga Ga" || got[ids["Quiet FM"]].Note != "The stream does not say what is playing." {
		t.Fatalf("now playing = %+v", got)
	}

	if w := req("GET", "/radio/"+ids["Quiet FM"]+"/logo.png", nil); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("logo: %d %s", w.Code, w.Header().Get("Content-Type"))
	}

	// Switching it off hides the line and stops reading streams.
	if w := req("POST", "/radio/now-playing", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("toggle: %d", w.Code)
	}
	if s, _ := st.Settings(ctx); s.RadioNowPlaying {
		t.Fatal("now playing still on")
	}
	if body := req("GET", "/radio", nil).Body.String(); strings.Contains(body, `class="now-playing"`) {
		t.Fatal("now playing line shown while switched off")
	}
	w = req("GET", "/radio/now-playing?id="+ids["Hits FM"], nil)
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("now playing while off = %s", w.Body.String())
	}
}
