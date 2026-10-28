package portal

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func TestRadioForPlayers(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.store.ImportChannels(ctx, []store.ImportEntry{{Kind: store.KindRadio, Name: "Radio 1",
		StreamURL: "https://icecast.example/radio1.mp3", Group: "Radio News", SourceID: "u1"}}); err != nil {
		t.Fatal(err)
	}
	radio, _ := f.store.Channels(ctx, store.ChannelFilter{Kind: store.KindRadio})
	cats, _ := f.store.CategoriesOf(ctx, store.KindRadio)
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	pkg := u.PackageIDs[0]
	p, _ := f.store.Package(ctx, pkg)
	p.CategoryIDs = append(p.CategoryIDs, cats[0].ID)
	if _, err := f.store.SavePackage(ctx, p); err != nil {
		t.Fatal(err)
	}

	// Radio stations are radio streams in their own categories, after TV.
	var categories []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_categories", &categories)
	if len(categories) != 2 || categories[1]["category_name"] != "Radio News" {
		t.Fatalf("categories = %v", categories)
	}
	var streams []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_streams&category_id="+fmt.Sprint(cats[0].ID), &streams)
	if len(streams) != 1 || streams[0]["name"] != "Radio 1" || streams[0]["stream_type"] != "radio_streams" {
		t.Fatalf("streams = %v", streams)
	}
	resp := f.get(t, fmt.Sprintf("/live/jan/secret1/%d.ts", radio[0].ID))
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://icecast.example/radio1.mp3" {
		t.Fatalf("redirect = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp = f.get(t, "/get.php?"+creds+"&type=m3u_plus")
	body, _ := io.ReadAll(resp.Body)
	pl := string(body)
	if !strings.Contains(pl, `group-title="Radio News" radio="true",Radio 1`) || strings.Contains(pl, `group-title="North" radio`) {
		t.Fatalf("playlist:\n%s", pl)
	}
}

func TestRadioAfterTV(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// A radio category that sorts before the TV categories, and a
	// station without a category.
	radioCat, err := f.store.SaveCategory(ctx, store.Category{Name: "Local", Kind: store.KindRadio, SortOrder: -10})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []store.Channel{
		{Name: "Radio A", Kind: store.KindRadio, StreamURL: "https://icecast.example/a.mp3", CategoryID: radioCat, Enabled: true},
		{Name: "Radio B", Kind: store.KindRadio, StreamURL: "https://icecast.example/b.mp3", Enabled: true},
	} {
		if _, err := f.store.SaveChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := f.store.AuthenticateUser(ctx, "jan", "secret1")
	p, _ := f.store.Package(ctx, u.PackageIDs[0])
	p.AllChannels = true
	if _, err := f.store.SavePackage(ctx, p); err != nil {
		t.Fatal(err)
	}

	var categories []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_categories", &categories)
	n := len(categories)
	if n < 3 || categories[n-2]["category_name"] != "Radio | Local" || categories[n-1]["category_name"] != "Radio" {
		t.Fatalf("categories = %v", categories)
	}
	var streams []map[string]any
	f.json(t, "/player_api.php?"+creds+"&action=get_live_streams", &streams)
	seenRadio := false
	for _, s := range streams {
		radio := s["stream_type"] == "radio_streams"
		if seenRadio && !radio {
			t.Fatalf("TV channel %v after radio: %v", s["name"], streams)
		}
		seenRadio = seenRadio || radio
	}
	if last := streams[len(streams)-1]; last["name"] != "Radio B" || last["category_id"] != "999999999" {
		t.Fatalf("last stream = %v", last)
	}

	resp := f.get(t, "/get.php?"+creds+"&type=m3u_plus")
	body, _ := io.ReadAll(resp.Body)
	pl := string(body)
	if !strings.Contains(pl, `group-title="Radio | Local" radio="true",Radio A`) ||
		!strings.Contains(pl, `group-title="Radio" radio="true",Radio B`) ||
		strings.Index(pl, "Radio A") < strings.LastIndex(pl, `group-title="North",`) {
		t.Fatalf("playlist:\n%s", pl)
	}

	if err := f.store.SetRadioLabel(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.json(t, "/player_api.php?"+creds+"&action=get_live_categories", &categories)
	if categories[len(categories)-2]["category_name"] != "Local" {
		t.Fatalf("categories without label = %v", categories)
	}
}
