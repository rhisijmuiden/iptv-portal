package portal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

const (
	magMAC     = "00:1A:79:00:00:01"
	unknownMAC = "00:1A:79:00:00:99"
)

func enableStalker(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	s, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.StalkerPortal = true
	if err := st.SaveSettings(ctx, s); err != nil {
		t.Fatal(err)
	}
}

func userID(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	users, err := st.Users(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == name {
			return u.ID
		}
	}
	t.Fatalf("no user %s", name)
	return 0
}

// stalker calls load.php the way a MAG box does: the MAC address in a
// cookie, the token in the Authorization header.
func (f fixture) stalker(t *testing.T, mac, query string, v any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/stalker_portal/server/load.php?"+query+"&JsHttpRequest=1-xml", nil)
	req.Header.Set("Cookie", "mac="+url.QueryEscape(mac)+"; stb_lang=en; timezone=Europe%2FAmsterdam")
	req.Header.Set("Authorization", "Bearer 0123456789ABCDEF0123456789ABCDEF")
	req.Header.Set("User-Agent", "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 2 rev: 250 Safari/533.3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		JS json.RawMessage `json:"js"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if v != nil {
		if err := json.Unmarshal(out.JS, v); err != nil {
			t.Fatalf("%s: %v in %s", query, err, out.JS)
		}
	}
	return resp.StatusCode
}

// play opens a play link without following the redirect.
func (f fixture) play(t *testing.T, cmd string) *http.Response {
	t.Helper()
	link := strings.TrimPrefix(cmd, "ffmpeg ")
	if !strings.HasPrefix(link, f.srv.URL+"/stalker/play/") {
		t.Fatalf("cmd = %q", cmd)
	}
	return f.get(t, strings.TrimPrefix(link, f.srv.URL))
}

func TestStalkerOff(t *testing.T) {
	f := setup(t)
	for _, p := range []string{"/stalker_portal/server/load.php?type=stb&action=handshake", "/portal.php?type=stb&action=handshake", "/c/"} {
		if resp := f.get(t, p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s with the Stalker portal off = %d", p, resp.StatusCode)
		}
	}
}

func TestStalkerLive(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enableStalker(t, f.store)
	jan := userID(t, f.store, "jan")
	dev, err := f.store.AddUserDevice(ctx, jan, "Living room", strings.ReplaceAll(magMAC, ":", "-"))
	if err != nil {
		t.Fatal(err)
	}

	var hs map[string]any
	f.stalker(t, magMAC, "type=stb&action=handshake&token=", &hs)
	if tok, _ := hs["token"].(string); len(tok) < 16 {
		t.Fatalf("handshake = %v", hs)
	}
	var profile map[string]any
	f.stalker(t, magMAC, "type=stb&action=get_profile&hd=1&stb_type=MAG250", &profile)
	if profile["status"] != float64(0) || profile["login"] != "jan" || profile["mac"] != magMAC {
		t.Fatalf("profile = %v", profile)
	}
	f.stalker(t, unknownMAC, "type=stb&action=get_profile", &profile)
	if profile["status"] != float64(2) {
		t.Fatalf("profile of unknown device = %v", profile)
	}
	if code := f.stalker(t, unknownMAC, "type=itv&action=get_all_channels", nil); code != http.StatusUnauthorized {
		t.Errorf("channels of unknown device = %d", code)
	}
	var info map[string]any
	f.stalker(t, magMAC, "type=account_info&action=get_main_info", &info)
	if info["fname"] != "jan" || info["phone"] != "Unlimited" {
		t.Errorf("main info = %v", info)
	}

	var genres []map[string]any
	f.stalker(t, magMAC, "type=itv&action=get_genres", &genres)
	if len(genres) != 2 || genres[0]["id"] != "*" || genres[1]["title"] != "North" {
		t.Fatalf("genres = %v", genres)
	}
	var list struct {
		Total int              `json:"total_items"`
		Data  []map[string]any `json:"data"`
	}
	f.stalker(t, magMAC, "type=itv&action=get_ordered_list&genre="+genres[1]["id"].(string)+"&p=1&sortby=number", &list)
	if list.Total != 1 || list.Data[0]["name"] != "NH" || list.Data[0]["xmltv_id"] != "nh.nl" {
		t.Fatalf("ordered list = %+v", list)
	}
	f.stalker(t, magMAC, "type=itv&action=get_all_channels", &list)
	if list.Total != 1 {
		t.Fatalf("all channels = %+v", list)
	}
	cmd := list.Data[0]["cmd"].(string)

	var link map[string]any
	f.stalker(t, magMAC, "type=itv&action=create_link&cmd="+url.QueryEscape(cmd), &link)
	good := link["cmd"].(string)
	resp := f.play(t, good)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://s.example/nh/index.m3u8" {
		t.Fatalf("play = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if !strings.HasSuffix(link["cmd"].(string), "/stream.m3u8") {
		t.Errorf("link = %s", link["cmd"])
	}
	if n := f.act.ActiveConnections(jan); n != 1 {
		t.Errorf("connections after playing = %d", n)
	}
	// A channel outside the package gets no link.
	f.stalker(t, magMAC, "type=itv&action=create_link&cmd="+url.QueryEscape("ffrt http://localhost/ch/"+itoa(f.gld)), &link)
	if link["cmd"] != "" {
		t.Errorf("link to a channel outside the package = %v", link)
	}

	var epgList []map[string]any
	f.stalker(t, magMAC, "type=itv&action=get_short_epg&ch_id="+itoa(f.nh)+"&size=10", &epgList)
	if len(epgList) != 2 || epgList[0]["name"] != "Nieuws" || epgList[0]["descr"] != "Vandaag" {
		t.Fatalf("short epg = %v", epgList)
	}
	var table struct {
		Selected int              `json:"selected_item"`
		Data     []map[string]any `json:"data"`
	}
	f.stalker(t, magMAC, "type=epg&action=get_simple_data_table&ch_id="+itoa(f.nh)+"&date=&p=0", &table)
	if len(table.Data) == 0 || table.Selected == 0 {
		t.Fatalf("day epg = %+v", table)
	}
	var epgInfo struct {
		Data map[string][]map[string]any `json:"data"`
	}
	f.stalker(t, magMAC, "type=itv&action=get_epg_info&period=3", &epgInfo)
	if len(epgInfo.Data[itoa(f.nh)]) != 2 {
		t.Fatalf("epg info = %+v", epgInfo)
	}

	// A tampered link is refused, and so is every link of a removed device.
	bad := strings.Replace(good, "/stalker/play/", "/stalker/play/x", 1)
	if resp := f.play(t, bad); resp.StatusCode != http.StatusForbidden {
		t.Errorf("tampered link = %d", resp.StatusCode)
	}
	if err := f.store.DeleteUserDevice(ctx, jan, dev.ID); err != nil {
		t.Fatal(err)
	}
	if resp := f.play(t, good); resp.StatusCode != http.StatusForbidden {
		t.Errorf("link of a removed device = %d", resp.StatusCode)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestStalkerLogin(t *testing.T) {
	f := setup(t)
	enableStalker(t, f.store)
	var ok bool
	f.stalker(t, unknownMAC, "type=stb&action=do_auth&login=jan&password=wrong", &ok)
	if ok {
		t.Fatal("do_auth with a wrong password succeeded")
	}
	f.stalker(t, unknownMAC, "type=stb&action=do_auth&login=jan&password=secret1", &ok)
	if !ok {
		t.Fatal("do_auth failed")
	}
	var profile map[string]any
	f.stalker(t, unknownMAC, "type=stb&action=get_profile", &profile)
	if profile["status"] != float64(0) || profile["login"] != "jan" {
		t.Fatalf("profile after do_auth = %v", profile)
	}
	devs, _ := f.store.UserDevices(context.Background(), userID(t, f.store, "jan"))
	if len(devs) != 1 || devs[0].MAC != unknownMAC {
		t.Fatalf("devices = %+v", devs)
	}

	// The address now belongs to jan and can not be added to another account.
	other, err := f.store.SaveUser(context.Background(), store.User{Username: "kees", Enabled: true}, "secret3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddUserDevice(context.Background(), other, "Box", unknownMAC); err == nil {
		t.Error("the same MAC address was registered for two accounts")
	}

	// A disabled account gets status 1.
	if err := f.store.SetUserEnabled(context.Background(), userID(t, f.store, "jan"), false); err != nil {
		t.Fatal(err)
	}
	f.stalker(t, unknownMAC, "type=stb&action=get_profile", &profile)
	if profile["status"] != float64(1) {
		t.Fatalf("profile of a disabled account = %v", profile)
	}
}

func TestStalkerVOD(t *testing.T) {
	f, _ := vodSetup(t, false)
	enableStalker(t, f.store)
	if _, err := f.store.AddUserDevice(context.Background(), userID(t, f.store, "piet"), "Box", magMAC); err != nil {
		t.Fatal(err)
	}
	var cats []map[string]any
	f.stalker(t, magMAC, "type=vod&action=get_categories", &cats)
	// All, Crime, Drama (movies) and Other (series).
	if len(cats) != 4 || cats[0]["id"] != "*" || cats[1]["title"] != "Crime" {
		t.Fatalf("vod categories = %v", cats)
	}
	var list struct {
		Total int              `json:"total_items"`
		Data  []map[string]any `json:"data"`
	}
	f.stalker(t, magMAC, "type=vod&action=get_ordered_list&category=*&p=1&sortby=name", &list)
	if list.Total != 3 {
		t.Fatalf("vod list = %+v", list)
	}
	names := []string{list.Data[0]["name"].(string), list.Data[1]["name"].(string), list.Data[2]["name"].(string)}
	if names[0] != "Heat" || names[1] != "Show – Season 1" || names[2] != "Show – Season 2" {
		t.Fatalf("vod names = %v", names)
	}
	f.stalker(t, magMAC, "type=vod&action=get_ordered_list&category="+cats[1]["id"].(string), &list)
	if list.Total != 1 || list.Data[0]["name"] != "Heat" {
		t.Fatalf("crime list = %+v", list)
	}

	var link map[string]any
	f.stalker(t, magMAC, "type=vod&action=create_link&cmd="+url.QueryEscape("ffmpeg http://localhost/vod/"+list.Data[0]["id"].(string)), &link)
	resp := f.play(t, link["cmd"].(string))
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || string(b) != string(movieData) {
		t.Fatalf("movie = %d %q", resp.StatusCode, b)
	}

	f.stalker(t, magMAC, "type=vod&action=get_ordered_list&sortby=name", &list)
	season2 := list.Data[2]
	if eps := season2["series"].([]any); len(eps) != 1 || eps[0] != float64(1) {
		t.Fatalf("episodes of season 2 = %v", season2["series"])
	}
	f.stalker(t, magMAC, "type=vod&action=create_link&series=1&cmd="+url.QueryEscape(season2["cmd"].(string)), &link)
	resp = f.play(t, link["cmd"].(string))
	if b, _ := io.ReadAll(resp.Body); string(b) != "episode" {
		t.Fatalf("episode = %d %q", resp.StatusCode, b)
	}

	// jan has no On Demand.
	if _, err := f.store.AddUserDevice(context.Background(), userID(t, f.store, "jan"), "Box", unknownMAC); err != nil {
		t.Fatal(err)
	}
	f.stalker(t, unknownMAC, "type=vod&action=get_categories", &cats)
	if len(cats) != 0 {
		t.Errorf("vod categories without On Demand = %v", cats)
	}
}

func TestStalkerParentalControls(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	enableStalker(t, f.store)
	jan := userID(t, f.store, "jan")
	if _, err := f.store.AddUserDevice(ctx, jan, "Box", magMAC); err != nil {
		t.Fatal(err)
	}
	cats, _ := f.store.Categories(ctx)
	for _, c := range cats {
		if c.Name == "North" {
			if err := f.store.SetCategoryLocked(ctx, jan, c.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	var list struct {
		Total int `json:"total_items"`
	}
	f.stalker(t, magMAC, "type=itv&action=get_all_channels", &list)
	if list.Total != 0 {
		t.Errorf("locked channels listed: %d", list.Total)
	}
	var link map[string]any
	f.stalker(t, magMAC, "type=itv&action=create_link&cmd="+url.QueryEscape("ffrt http://localhost/ch/"+itoa(f.nh)), &link)
	if link["cmd"] != "" {
		t.Errorf("link to a locked channel = %v", link)
	}
}

func TestStalkerVODParental(t *testing.T) {
	f, _ := vodSetup(t, false)
	ctx := context.Background()
	enableStalker(t, f.store)
	piet := userID(t, f.store, "piet")
	if _, err := f.store.AddUserDevice(ctx, piet, "Box", magMAC); err != nil {
		t.Fatal(err)
	}
	f.store.SetLockedMedia(ctx, piet, []store.MediaRef{{Kind: "movies", Name: "crime"}})
	var list struct {
		Data []map[string]any `json:"data"`
	}
	f.stalker(t, magMAC, "type=vod&action=get_ordered_list&sortby=name", &list)
	for _, it := range list.Data {
		if it["name"] == "Heat" {
			t.Errorf("locked movie listed")
		}
	}
	var cats []map[string]any
	f.stalker(t, magMAC, "type=vod&action=get_categories", &cats)
	for _, c := range cats {
		if c["title"] == "Crime" {
			t.Errorf("locked category listed: %v", cats)
		}
	}
}
