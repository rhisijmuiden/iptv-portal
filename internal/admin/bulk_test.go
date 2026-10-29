package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestChannelBulkKeepsFilter(t *testing.T) {
	h, st := newTestHandler(t, "")
	req := loggedIn(t, h, st)
	ctx := context.Background()
	add := func(kind, name string) int64 {
		t.Helper()
		id, err := st.SaveChannel(ctx, store.Channel{Kind: kind, Name: name, StreamURL: "https://example.com/" + name, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c := add(store.KindTV, "a"), add(store.KindTV, "b"), add(store.KindTV, "c")
	radio := add(store.KindRadio, "r")
	news, _ := st.EnsureCategory(ctx, store.KindTV, "News")
	const back = "/channels?health=failing&q=x"

	body := req("GET", "/channels?health=unchecked&q=a", nil).Body.String()
	for _, want := range []string{`class="select-all"`, `action="/channels/bulk"`, `name="back" value="/channels?health=unchecked&amp;q=a"`,
		`href="/channels/` + itoa(a) + `?back=%2fchannels%3fhealth%3dunchecked%26q%3da"`} {
		if !strings.Contains(body, want) {
			t.Errorf("channel list is missing %q", want)
		}
	}

	location := func(w interface{ Header() http.Header }) string { return w.Header().Get("Location") }
	w := req("POST", "/channels/bulk", url.Values{"action": {"category"}, "category_id": {itoa(news)},
		"id": {itoa(a), itoa(b), itoa(radio)}, "back": {back}})
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(location(w), "/channels?") ||
		!strings.Contains(location(w), "health=failing") || !strings.Contains(location(w), "Channels+moved+to+News%3A+2") {
		t.Fatalf("move: %d %q", w.Code, location(w))
	}
	if ch, _ := st.Channel(ctx, radio); ch.CategoryID != 0 {
		t.Error("a radio station was moved from the channel list")
	}

	w = req("POST", "/channels/bulk", url.Values{"action": {"delete"}, "id": {itoa(a), itoa(c)}, "back": {back}})
	if !strings.Contains(location(w), "health=failing") || !strings.Contains(location(w), "Channels+deleted%3A+2") {
		t.Fatalf("delete: %q", location(w))
	}
	if left, _ := st.Channels(ctx, store.ChannelFilter{Kind: store.KindTV}); len(left) != 1 || left[0].ID != b {
		t.Errorf("left after delete: %+v", left)
	}

	if w = req("POST", "/channels/bulk", url.Values{"action": {"delete"}, "back": {back}}); !strings.Contains(location(w), "err=Select+at+least") {
		t.Errorf("empty selection: %q", location(w))
	}

	// Deleting a single channel and saving an edit also return to the filter.
	if w = req("POST", "/channels/"+itoa(b)+"/delete", url.Values{"back": {back}}); !strings.Contains(location(w), "health=failing") {
		t.Errorf("single delete went to %q", location(w))
	}
	if w = req("POST", "/radio/"+itoa(radio), url.Values{"name": {"r"}, "stream_url": {"https://example.com/r.mp3"},
		"back": {"/radio?category=-1"}}); !strings.Contains(location(w), "category=-1") {
		t.Errorf("save went to %q", location(w))
	}
	if body := req("GET", "/radio/"+itoa(radio)+"?back=/radio?category%3D-1", nil).Body.String(); !strings.Contains(body, `href="/radio?category=-1"`) {
		t.Error("the form does not link back to the filtered list")
	}
	// Only local paths are followed.
	if w = req("POST", "/radio/bulk", url.Values{"action": {"enable"}, "id": {itoa(radio)}, "back": {"//evil.example/"}}); !strings.HasPrefix(location(w), "/radio?") {
		t.Errorf("bulk went to %q", location(w))
	}
}
