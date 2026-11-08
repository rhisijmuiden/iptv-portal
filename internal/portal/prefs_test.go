package portal

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/epg"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// prefsFixture is a user "kim" with all channels: TV groups News (n1, n2)
// and Kids (k1), radio group Music (r1).
type prefsFixture struct {
	fixture
	user  store.User
	ids   map[string]int64 // channel ids by name
	cats  map[string]int64 // category ids by name
	creds string
}

func prefsSetup(t *testing.T) prefsFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.ImportChannels(ctx, []store.ImportEntry{
		{Name: "n1", StreamURL: "https://s.example/n1.m3u8", EPGID: "n1.nl", Group: "News"},
		{Name: "n2", StreamURL: "https://s.example/n2.m3u8", Group: "News"},
		{Name: "k1", StreamURL: "https://s.example/k1.m3u8", EPGID: "k1.nl", Group: "Kids"},
		{Name: "r1", StreamURL: "https://s.example/r1.mp3", Group: "Music", Kind: store.KindRadio},
	}); err != nil {
		t.Fatal(err)
	}
	f := prefsFixture{ids: map[string]int64{}, cats: map[string]int64{}, creds: "username=kim&password=secret3"}
	chs, _ := st.Channels(ctx, store.ChannelFilter{})
	for _, c := range chs {
		f.ids[c.Name] = c.ID
		f.cats[c.Category] = c.CategoryID
	}
	if len(f.ids) != 4 {
		t.Fatalf("channels = %+v", chs)
	}
	pkg, err := st.SavePackage(ctx, store.Package{Name: "All", AllChannels: true})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.SaveUser(ctx, store.User{Username: "kim", Enabled: true, PackageIDs: []int64{pkg}}, "secret3")
	if err != nil {
		t.Fatal(err)
	}
	f.user, _ = st.User(ctx, id)
	log := slog.New(slog.DiscardHandler)
	h, err := New(st, epg.New(log, t.TempDir()), activity.New(), nil, log)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	f.store = st
	return f
}

// streams returns "name@category" of the live streams, in order.
func (f prefsFixture) streams(t *testing.T) []string {
	t.Helper()
	var out []map[string]any
	f.json(t, "/player_api.php?"+f.creds+"&action=get_live_streams", &out)
	var names []string
	for _, s := range out {
		names = append(names, fmt.Sprintf("%v@%v", s["name"], s["category_id"]))
	}
	return names
}

func (f prefsFixture) categories(t *testing.T) []string {
	t.Helper()
	var out []map[string]any
	f.json(t, "/player_api.php?"+f.creds+"&action=get_live_categories", &out)
	var names []string
	for _, c := range out {
		names = append(names, fmt.Sprint(c["category_name"]))
	}
	return names
}

func (f prefsFixture) cat(name string) string { return strconv.FormatInt(f.cats[name], 10) }

func TestFavouritesAndOrderInPlayer(t *testing.T) {
	f := prefsSetup(t)
	ctx := context.Background()
	if got := strings.Join(f.categories(t), ","); got != "Kids,News,Radio | Music" {
		t.Fatalf("default categories = %s", got)
	}
	f.store.SetFavourite(ctx, f.user.ID, f.ids["n2"], true)
	f.store.SetFavourite(ctx, f.user.ID, f.ids["r1"], true)
	if err := f.store.SetCategoryOrder(ctx, f.user.ID, []int64{f.cats["News"], f.cats["Kids"]}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.categories(t), ","); got != "Favourites,News,Kids,Radio | Music" {
		t.Errorf("categories = %s", got)
	}
	fav := strconv.Itoa(favouritesCategory)
	want := strings.Join([]string{"n2@" + fav, "r1@" + fav, "n1@" + f.cat("News"), "n2@" + f.cat("News"),
		"k1@" + f.cat("Kids"), "r1@" + f.cat("Music")}, ",")
	if got := strings.Join(f.streams(t), ","); got != want {
		t.Errorf("streams = %s, want %s", got, want)
	}
	// One category only.
	var one []map[string]any
	f.json(t, "/player_api.php?"+f.creds+"&action=get_live_streams&category_id="+fav, &one)
	if len(one) != 2 || one[0]["name"] != "n2" || one[1]["stream_type"] != "radio_streams" {
		t.Errorf("favourites = %v", one)
	}
	// Favourites first in the M3U playlist too.
	resp := f.get(t, "/get.php?"+f.creds+"&type=m3u_plus")
	var groups []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if i := strings.Index(sc.Text(), `group-title="`); i >= 0 {
			rest := sc.Text()[i+len(`group-title="`):]
			groups = append(groups, rest[:strings.Index(rest, `"`)])
		}
	}
	if got := strings.Join(groups, ","); got != "Favourites,Favourites,News,News,Kids,Radio | Music" {
		t.Errorf("m3u groups = %s", got)
	}

	// Moving favourites, turning the group off, hiding a group.
	f.store.SetFavouriteOrder(ctx, f.user.ID, []int64{f.ids["r1"], f.ids["n2"]})
	if got := f.streams(t); got[0] != "r1@"+fav {
		t.Errorf("after move: %v", got)
	}
	f.store.SetFavouritesGroup(ctx, f.user.ID, false)
	f.store.SetCategoryHidden(ctx, f.user.ID, f.cats["News"], true)
	if got := strings.Join(f.categories(t), ","); got != "Kids,Radio | Music" {
		t.Errorf("categories without favourites and News = %s", got)
	}
	// A hidden group can still be played.
	if resp := f.get(t, fmt.Sprintf("/live/kim/secret3/%d.m3u8", f.ids["n1"])); resp.StatusCode != http.StatusFound {
		t.Errorf("hidden channel playback: %d", resp.StatusCode)
	}
	// Removed channels drop out of the favourites.
	f.store.DeleteChannel(ctx, f.ids["r1"])
	p, _ := f.store.Preferences(ctx, f.user.ID)
	if len(p.Favourites) != 1 || p.Favourites[0] != f.ids["n2"] {
		t.Errorf("favourites after delete = %v", p.Favourites)
	}
}

func TestParentalLocksInPlayer(t *testing.T) {
	f := prefsSetup(t)
	ctx := context.Background()
	f.store.SetFavourite(ctx, f.user.ID, f.ids["k1"], true)
	if err := f.store.SetCategoryLocked(ctx, f.user.ID, f.cats["Kids"], true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.categories(t), ","); got != "News,Radio | Music" {
		t.Errorf("locked categories = %s", got)
	}
	for _, s := range f.streams(t) {
		if strings.HasPrefix(s, "k1@") {
			t.Errorf("locked channel listed: %v", f.streams(t))
		}
	}
	if resp := f.get(t, fmt.Sprintf("/live/kim/secret3/%d.m3u8", f.ids["k1"])); resp.StatusCode != http.StatusNotFound {
		t.Errorf("locked channel playback: %d", resp.StatusCode)
	}
	resp := f.get(t, "/xmltv.php?"+f.creds)
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		b.WriteString(sc.Text())
	}
	if strings.Contains(b.String(), "k1.nl") || !strings.Contains(b.String(), "n1.nl") {
		t.Errorf("guide = %s", b.String())
	}

	until := time.Now().Add(time.Hour)
	f.store.SetUnlockedUntil(ctx, f.user.ID, &until)
	if got := strings.Join(f.categories(t), ","); got != "Favourites,Kids,News,Radio | Music" {
		t.Errorf("unlocked categories = %s", got)
	}
	if resp := f.get(t, fmt.Sprintf("/live/kim/secret3/%d.m3u8", f.ids["k1"])); resp.StatusCode != http.StatusFound {
		t.Errorf("unlocked playback: %d", resp.StatusCode)
	}
	past := time.Now().Add(-time.Minute)
	f.store.SetUnlockedUntil(ctx, f.user.ID, &past)
	if got := strings.Join(f.categories(t), ","); got != "News,Radio | Music" {
		t.Errorf("expired unlock categories = %s", got)
	}
}

func TestAccountMyChannels(t *testing.T) {
	f := prefsSetup(t)
	ctx := context.Background()
	b := login(t, f.fixture, "kim", "secret3")
	code, _, body := b.do("GET", "/account/channels", nil)
	if code != http.StatusOK || !strings.Contains(body, "You have no favourites yet") || !strings.Contains(body, "Kids") {
		t.Fatalf("my channels: %d\n%s", code, body)
	}
	csrf := b.csrf("/account/channels")
	post := func(path string, v url.Values) (int, string) {
		t.Helper()
		v.Set("csrf", csrf)
		code, loc, _ := b.do("POST", path, v)
		return code, loc
	}
	id := func(name string) string { return strconv.FormatInt(f.ids[name], 10) }
	if code, loc := post("/account/channels/favourite", url.Values{"channel": {id("n1")}, "on": {"1"}}); code != http.StatusSeeOther ||
		!strings.Contains(loc, "open="+f.cat("News")) || !strings.HasSuffix(loc, "#group-"+f.cat("News")) {
		t.Errorf("favourite: %d %s", code, loc)
	}
	post("/account/channels/favourite", url.Values{"channel": {id("k1")}, "on": {"1"}})
	post("/account/channels/favourites/move", url.Values{"channel": {id("k1")}, "dir": {"up"}})
	p, _ := f.store.Preferences(ctx, f.user.ID)
	if len(p.Favourites) != 2 || p.Favourites[0] != f.ids["k1"] {
		t.Errorf("favourites = %v", p.Favourites)
	}
	// Channels outside the user's packages cannot become favourites.
	other, _ := f.store.SaveChannel(ctx, store.Channel{Name: "x", StreamURL: "https://s.example/x.m3u8", Enabled: false})
	if code, _ := post("/account/channels/favourite", url.Values{"channel": {strconv.FormatInt(other, 10)}, "on": {"1"}}); code != http.StatusNotFound {
		t.Errorf("favourite of a channel the user does not have: %d", code)
	}
	// Order: News moves above Kids; radio stays after TV.
	post("/account/channels/groups/move", url.Values{"category": {f.cat("News")}, "dir": {"up"}})
	post("/account/channels/groups/move", url.Values{"category": {f.cat("Music")}, "dir": {"up"}})
	if got := strings.Join(f.categories(t), ","); got != "Favourites,News,Kids,Radio | Music" {
		t.Errorf("categories = %s", got)
	}
	post("/account/channels/groups/hide", url.Values{"category": {f.cat("Kids")}, "hidden": {"1"}})
	post("/account/channels/favourites/group", url.Values{})
	if got := strings.Join(f.categories(t), ","); got != "News,Radio | Music" {
		t.Errorf("categories = %s", got)
	}
	_, _, body = b.do("GET", "/account/channels", nil)
	if !strings.Contains(body, "Standard order") || !strings.Contains(body, `class="star on"`) {
		t.Errorf("page after changes:\n%s", body)
	}
	post("/account/channels/groups/reset", url.Values{})
	post("/account/channels/groups/hide", url.Values{"category": {f.cat("Kids")}, "hidden": {"0"}})
	if got := strings.Join(f.categories(t), ","); got != "Kids,News,Radio | Music" {
		t.Errorf("categories after reset = %s", got)
	}
	// The CSRF token is checked.
	if code, _, _ := b.do("POST", "/account/channels/groups/reset", url.Values{"csrf": {"nope"}}); code != http.StatusForbidden {
		t.Errorf("without CSRF: %d", code)
	}
}

func TestAccountParental(t *testing.T) {
	f := prefsSetup(t)
	ctx := context.Background()
	b := login(t, f.fixture, "kim", "secret3")
	_, _, body := b.do("GET", "/account/parental", nil)
	if !strings.Contains(body, "Set a PIN") {
		t.Fatalf("parental page without PIN:\n%s", body)
	}
	csrf := b.csrf("/account/parental")
	post := func(path string, v url.Values) (int, string, string) {
		t.Helper()
		v.Set("csrf", csrf)
		return b.do("POST", path, v)
	}
	// Locking needs a PIN first.
	if code, _, _ := post("/account/parental/save", url.Values{"group": {f.cat("Kids")}}); code != http.StatusBadRequest {
		t.Errorf("save without PIN: %d", code)
	}
	if code, _, body := post("/account/parental/pin", url.Values{"pin": {"12a4"}, "confirm": {"12a4"}}); code != http.StatusBadRequest ||
		!strings.Contains(body, "4 to 8 digits") {
		t.Errorf("bad PIN: %d", code)
	}
	if code, _, _ := post("/account/parental/pin", url.Values{"pin": {"2468"}, "confirm": {"2468"}}); code != http.StatusSeeOther {
		t.Fatalf("set PIN: %d", code)
	}
	// Setting the PIN opens the settings.
	if code, _, _ := post("/account/parental/save", url.Values{"group": {f.cat("Kids"), "999"}}); code != http.StatusSeeOther {
		t.Fatalf("save: %d", code)
	}
	if got := strings.Join(f.categories(t), ","); got != "News,Radio | Music" {
		t.Errorf("categories after locking = %s", got)
	}
	_, _, body = b.do("GET", "/account/parental", nil)
	if !strings.Contains(body, "1 channel group locked") || !strings.Contains(body, "Unlock for 3 hours") {
		t.Errorf("open page:\n%s", body)
	}
	// Closing asks for the PIN again; a wrong PIN is refused and counted.
	post("/account/parental/close", url.Values{})
	if code, _, _ := post("/account/parental/unlock", url.Values{"hours": {"3"}}); code != http.StatusForbidden {
		t.Errorf("unlock without PIN: %d", code)
	}
	if code, _, body := post("/account/parental/open", url.Values{"pin": {"0000"}}); code != http.StatusUnauthorized || !strings.Contains(body, "Wrong PIN.") {
		t.Errorf("wrong PIN: %d", code)
	}
	if code, _, _ := post("/account/parental/open", url.Values{"pin": {"2468"}}); code != http.StatusSeeOther {
		t.Errorf("right PIN: %d", code)
	}
	if code, _, _ := post("/account/parental/unlock", url.Values{"hours": {"3"}}); code != http.StatusSeeOther {
		t.Errorf("unlock: %d", code)
	}
	p, _ := f.store.Preferences(ctx, f.user.ID)
	if !p.Parental.Unlocked(time.Now().Add(2*time.Hour)) || p.Parental.Unlocked(time.Now().Add(4*time.Hour)) {
		t.Errorf("unlocked until %v", p.Parental.UnlockedUntil)
	}
	if got := strings.Join(f.categories(t), ","); got != "Kids,News,Radio | Music" {
		t.Errorf("unlocked categories = %s", got)
	}
	// Locking again needs no PIN.
	post("/account/parental/close", url.Values{})
	if code, _, _ := post("/account/parental/lock", url.Values{}); code != http.StatusSeeOther {
		t.Errorf("lock: %d", code)
	}
	if got := strings.Join(f.categories(t), ","); got != "News,Radio | Music" {
		t.Errorf("categories after locking again = %s", got)
	}
	// Five wrong PINs block further tries, even from another browser.
	for i := 0; i < 5; i++ {
		post("/account/parental/open", url.Values{"pin": {"1111"}})
	}
	b2 := login(t, f.fixture, "kim", "secret3")
	if code, _, _ := b2.do("POST", "/account/parental/open", url.Values{"csrf": {b2.csrf("/account/parental")}, "pin": {"2468"}}); code != http.StatusTooManyRequests {
		t.Errorf("after 5 wrong PINs: %d", code)
	}
	// The admin removes a forgotten PIN; the locks stay.
	if err := f.store.RemoveParentalPIN(ctx, f.user.ID); err != nil {
		t.Fatal(err)
	}
	p, _ = f.store.Preferences(ctx, f.user.ID)
	if p.Parental.PINSet || !p.Categories[f.cats["Kids"]].Locked {
		t.Errorf("after removing the PIN: %+v", p)
	}
}

func TestVODParental(t *testing.T) {
	f, _ := vodSetup(t, false)
	ctx := context.Background()
	u, _ := f.store.Users(ctx, "piet")
	id := u[0].ID
	// An age limit keeps titles without a rating.
	f.store.SetMaxAge(ctx, id, 12)
	var movies []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_streams", &movies)
	if len(movies) != 1 {
		t.Fatalf("movies with age limit = %v", movies)
	}
	movie := int64(movies[0]["stream_id"].(float64))
	// Locking one genre of a movie hides it.
	f.store.SetLockedMedia(ctx, id, []store.MediaRef{{Kind: "movies", Name: "crime"}})
	var cats []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_categories", &cats)
	if len(cats) != 1 || cats[0]["category_name"] != "Drama" {
		t.Errorf("categories = %v", cats)
	}
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_vod_streams", &movies)
	if len(movies) != 0 {
		t.Errorf("locked movie listed: %v", movies)
	}
	var info map[string]any
	f.json(t, fmt.Sprintf("/player_api.php?%s&action=get_vod_info&vod_id=%d", vodCreds, movie), &info)
	if len(info) != 0 {
		t.Errorf("locked movie info = %v", info)
	}
	if resp := f.get(t, fmt.Sprintf("/movie/piet/secret2/%d.mkv", movie)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("locked movie playback: %d", resp.StatusCode)
	}
	// Series are not affected by a movie genre.
	var series []map[string]any
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_series", &series)
	if len(series) != 1 {
		t.Errorf("series = %v", series)
	}
	// Locking "Other" series hides the series and its episodes.
	f.store.SetLockedMedia(ctx, id, []store.MediaRef{{Kind: "shows", Name: "Other"}})
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_series", &series)
	if len(series) != 0 {
		t.Errorf("locked series listed: %v", series)
	}
	until := time.Now().Add(time.Hour)
	f.store.SetUnlockedUntil(ctx, id, &until)
	f.json(t, "/player_api.php?"+vodCreds+"&action=get_series", &series)
	if len(series) != 1 {
		t.Errorf("unlocked series = %v", series)
	}
}

func TestAgeRating(t *testing.T) {
	for in, want := range map[string]int{
		"PG-13": 13, "R": 17, "TV-MA": 17, "TV-Y7": 7, "G": 0, "NL-12": 12, "nl/16": 16, "FSK 16": 16,
		"16+": 16, "AL": 0, "12A": 12, "GB-15": 15, "de-18": 18, "US-PG": 8,
	} {
		if got, ok := ageRating(in); !ok || got != want {
			t.Errorf("ageRating(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "NR", "Unrated", "Not Rated", "1999"} {
		if _, ok := ageRating(in); ok {
			t.Errorf("ageRating(%q) is known", in)
		}
	}
}
