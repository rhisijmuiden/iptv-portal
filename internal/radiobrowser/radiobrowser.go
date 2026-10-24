// Package radiobrowser searches the public radio station database of
// Radio Browser (https://www.radio-browser.info) so stations can be picked
// from it in the admin panel.
package radiobrowser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

// fallbackServers are used when the server list can't be looked up in DNS.
var fallbackServers = []string{"de1.api.radio-browser.info", "de2.api.radio-browser.info", "fi1.api.radio-browser.info"}

// listsMaxAge is how long the country, language and tag lists are kept.
const listsMaxAge = 12 * time.Hour

// Station is one radio station.
type Station struct {
	UUID        string
	Name        string
	URL         string // the playable stream; playlists are already resolved
	Homepage    string
	Favicon     string
	Tags        []string
	Country     string
	CountryCode string
	Languages   []string
	Codec       string
	Bitrate     int // kbit/s, 0 when unknown
	Votes       int
	Clicks      int
	HLS         bool
	// Working is false when Radio Browser's last check of the stream
	// failed.
	Working bool
}

// Item is a country, language or tag with the number of stations.
type Item struct {
	Code  string // ISO code for countries and languages, the name for tags
	Name  string
	Count int
}

// Orders a search can be sorted by.
const (
	OrderVotes   = "votes"
	OrderClicks  = "clickcount"
	OrderName    = "name"
	OrderBitrate = "bitrate"
)

// Filter narrows a search.
type Filter struct {
	Name        string
	CountryCode string // ISO 3166-1 code, e.g. "NL"
	Language    string // language name as Radio Browser writes it, e.g. "dutch"
	Tag         string
	Order       string // one of the Order constants; OrderVotes by default
	Limit       int
	Offset      int
}

// Client talks to the Radio Browser API.
type Client struct {
	// BaseURL, when set, is the only server used, e.g. a test server.
	// Otherwise a server is picked from the list Radio Browser publishes
	// in DNS.
	BaseURL string
	HTTP    *http.Client

	mu     sync.Mutex
	server string // base URL of the server that answered last
	lists  map[string]cachedList
}

type cachedList struct {
	items  []Item
	loaded time.Time
}

// NewClient returns a client for the public Radio Browser API.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// servers returns the base URLs to try, the one that worked last first.
func (c *Client) servers(ctx context.Context) []string {
	if c.BaseURL != "" {
		return []string{strings.TrimRight(c.BaseURL, "/")}
	}
	c.mu.Lock()
	last := c.server
	c.mu.Unlock()
	var hosts []string
	if _, srvs, err := net.DefaultResolver.LookupSRV(ctx, "api", "tcp", "radio-browser.info"); err == nil {
		for _, s := range srvs {
			hosts = append(hosts, strings.TrimSuffix(s.Target, "."))
		}
	}
	if len(hosts) == 0 {
		hosts = append(hosts, fallbackServers...)
	}
	// Spread the load over the servers, as Radio Browser asks.
	rand.Shuffle(len(hosts), func(i, j int) { hosts[i], hosts[j] = hosts[j], hosts[i] })
	out := make([]string, 0, len(hosts)+1)
	if last != "" {
		out = append(out, last)
	}
	for _, h := range hosts {
		if u := "https://" + h; u != last {
			out = append(out, u)
		}
	}
	return out
}

// get calls path with query on the first server that answers and decodes
// the JSON answer into v.
func (c *Client) get(ctx context.Context, path string, query url.Values, v any) error {
	var lastErr error
	for _, base := range c.servers(ctx) {
		err := c.getFrom(ctx, base, path, query, v)
		if err == nil {
			c.mu.Lock()
			c.server = base
			c.mu.Unlock()
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("Radio Browser: %w", lastErr)
}

func (c *Client) getFrom(ctx context.Context, base, path string, query url.Values, v any) error {
	u := base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	// Radio Browser asks every app to send a speaking User-Agent.
	req.Header.Set("User-Agent", "iptv-portal/"+version.Version)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

type apiStation struct {
	UUID        string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLResolved string `json:"url_resolved"`
	Homepage    string `json:"homepage"`
	Favicon     string `json:"favicon"`
	Tags        string `json:"tags"`
	Country     string `json:"country"`
	CountryCode string `json:"countrycode"`
	Language    string `json:"language"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	Votes       int    `json:"votes"`
	Clicks      int    `json:"clickcount"`
	HLS         int    `json:"hls"`
	LastCheckOK int    `json:"lastcheckok"`
}

func (a apiStation) station() Station {
	s := Station{UUID: a.UUID, Name: strings.TrimSpace(a.Name), URL: strings.TrimSpace(a.URLResolved),
		Homepage: strings.TrimSpace(a.Homepage), Favicon: strings.TrimSpace(a.Favicon), Tags: splitList(a.Tags),
		Country: a.Country, CountryCode: strings.ToUpper(a.CountryCode), Languages: splitList(a.Language),
		Codec: a.Codec, Bitrate: a.Bitrate, Votes: a.Votes, Clicks: a.Clicks, HLS: a.HLS == 1,
		Working: a.LastCheckOK == 1}
	if s.URL == "" {
		s.URL = strings.TrimSpace(a.URL)
	}
	return s
}

func splitList(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" && !seen[strings.ToLower(p)] {
			seen[strings.ToLower(p)] = true
			out = append(out, p)
		}
	}
	return out
}

// Search returns the stations matching f. Stations whose stream failed
// Radio Browser's last check are left out.
func (c *Client) Search(ctx context.Context, f Filter) ([]Station, error) {
	q := url.Values{"hidebroken": {"true"}}
	if v := strings.TrimSpace(f.Name); v != "" {
		q.Set("name", v)
	}
	if v := strings.TrimSpace(f.CountryCode); v != "" {
		q.Set("countrycode", strings.ToUpper(v))
	}
	if v := strings.TrimSpace(f.Language); v != "" {
		q.Set("language", v)
		q.Set("languageExact", "true")
	}
	if v := strings.TrimSpace(f.Tag); v != "" {
		q.Set("tag", v)
		q.Set("tagExact", "true")
	}
	order := f.Order
	switch order {
	case OrderVotes, OrderClicks, OrderName, OrderBitrate:
	default:
		order = OrderVotes
	}
	q.Set("order", order)
	if order != OrderName {
		q.Set("reverse", "true")
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	if f.Offset > 0 {
		q.Set("offset", strconv.Itoa(f.Offset))
	}
	var list []apiStation
	if err := c.get(ctx, "/json/stations/search", q, &list); err != nil {
		return nil, err
	}
	return stations(list), nil
}

// ByUUID returns the stations with the given UUIDs, in no particular order.
func (c *Client) ByUUID(ctx context.Context, uuids []string) ([]Station, error) {
	var out []Station
	// Keep the URL short enough for every server.
	for len(uuids) > 0 {
		n := min(len(uuids), 50)
		var list []apiStation
		if err := c.get(ctx, "/json/stations/byuuid", url.Values{"uuids": {strings.Join(uuids[:n], ",")}}, &list); err != nil {
			return nil, err
		}
		out = append(out, stations(list)...)
		uuids = uuids[n:]
	}
	return out, nil
}

func stations(list []apiStation) []Station {
	out := make([]Station, 0, len(list))
	for _, a := range list {
		if a.UUID == "" {
			continue
		}
		out = append(out, a.station())
	}
	return out
}

// Countries returns the countries that have stations, sorted by name.
func (c *Client) Countries(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "countries", func() ([]Item, error) {
		var list []struct {
			Name  string `json:"name"`
			Code  string `json:"iso_3166_1"`
			Count int    `json:"stationcount"`
		}
		if err := c.get(ctx, "/json/countries", url.Values{"hidebroken": {"true"}}, &list); err != nil {
			return nil, err
		}
		var out []Item
		for _, l := range list {
			if l.Code != "" && l.Count > 0 {
				out = append(out, Item{Code: strings.ToUpper(l.Code), Name: l.Name, Count: l.Count})
			}
		}
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
		return out, nil
	})
}

// Languages returns the most used languages, sorted by name.
func (c *Client) Languages(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "languages", func() ([]Item, error) {
		var list []struct {
			Name  string `json:"name"`
			Code  string `json:"iso_639"`
			Count int    `json:"stationcount"`
		}
		q := url.Values{"hidebroken": {"true"}, "order": {"stationcount"}, "reverse": {"true"}, "limit": {"150"}}
		if err := c.get(ctx, "/json/languages", q, &list); err != nil {
			return nil, err
		}
		var out []Item
		for _, l := range list {
			if l.Name != "" && l.Count > 0 {
				out = append(out, Item{Code: l.Code, Name: l.Name, Count: l.Count})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out, nil
	})
}

// Tags returns the most used tags (genres), most used first.
func (c *Client) Tags(ctx context.Context) ([]Item, error) {
	return c.list(ctx, "tags", func() ([]Item, error) {
		var list []struct {
			Name  string `json:"name"`
			Count int    `json:"stationcount"`
		}
		q := url.Values{"hidebroken": {"true"}, "order": {"stationcount"}, "reverse": {"true"}, "limit": {"300"}}
		if err := c.get(ctx, "/json/tags", q, &list); err != nil {
			return nil, err
		}
		var out []Item
		for _, l := range list {
			if l.Name != "" && l.Count > 0 {
				out = append(out, Item{Code: l.Name, Name: l.Name, Count: l.Count})
			}
		}
		return out, nil
	})
}

// list returns a cached list, loading it when it is missing or old. An old
// copy is kept when Radio Browser can't be reached.
func (c *Client) list(ctx context.Context, key string, load func() ([]Item, error)) ([]Item, error) {
	c.mu.Lock()
	cached, ok := c.lists[key]
	c.mu.Unlock()
	if ok && time.Since(cached.loaded) < listsMaxAge {
		return cached.items, nil
	}
	items, err := load()
	if err != nil {
		if ok {
			return cached.items, nil
		}
		return nil, err
	}
	c.mu.Lock()
	if c.lists == nil {
		c.lists = map[string]cachedList{}
	}
	c.lists[key] = cachedList{items: items, loaded: time.Now()}
	c.mu.Unlock()
	return items, nil
}

// ResolvePlaylist returns the first stream in a PLS, M3U or ASX playlist
// at u, which is how many stations publish their stream. Any other URL,
// including HLS (.m3u8), is returned unchanged; so is u when the playlist
// can't be read.
func ResolvePlaylist(ctx context.Context, client *http.Client, u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	ext := strings.ToLower(p.Path)
	if i := strings.LastIndex(ext, "."); i >= 0 {
		ext = ext[i:]
	} else {
		return u
	}
	if ext != ".pls" && ext != ".m3u" && ext != ".asx" {
		return u
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return u
	}
	req.Header.Set("User-Agent", "iptv-portal/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return u
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return u
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 64<<10))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case ext == ".pls":
			// File1=http://…
			if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(strings.ToLower(k), "file") {
				line = strings.TrimSpace(v)
			} else {
				continue
			}
		case ext == ".asx":
			// <ref href="http://…"/>
			i := strings.Index(strings.ToLower(line), "href=")
			if i < 0 {
				continue
			}
			line = strings.Trim(line[i+5:], `"'/> `)
			if j := strings.IndexAny(line, `"'`); j >= 0 {
				line = line[:j]
			}
		}
		if ref, err := url.Parse(line); err == nil && (ref.Scheme == "http" || ref.Scheme == "https") && ref.Host != "" {
			return ref.String()
		}
	}
	return u
}
