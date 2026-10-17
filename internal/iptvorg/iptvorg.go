// Package iptvorg reads the public channel database of iptv-org
// (https://iptv-org.github.io) so channels can be picked from it in the
// admin panel.
package iptvorg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is where iptv-org publishes its API files.
const DefaultBaseURL = "https://iptv-org.github.io/api"

// maxAge is how long a downloaded catalog is used before it is reloaded.
const maxAge = 12 * time.Hour

// Stream is one playable URL of a channel.
type Stream struct {
	URL     string
	Title   string
	Feed    string
	Quality string
	Labels  []string
	// NeedsHeaders is set when the stream only plays with a specific
	// User-Agent or Referer, which clients connecting directly don't send.
	NeedsHeaders bool
}

// Channel is a channel with at least one stream.
type Channel struct {
	ID         string // iptv-org channel id, e.g. "NPO1.nl"; empty for unlisted streams
	Name       string
	Country    string // ISO code, e.g. "NL"
	Categories []string
	Logo       string
	Streams    []Stream
}

// EPGID returns the tvg-id for a stream of this channel, the way iptv-org
// playlists write it ("NPO1.nl@SD").
func (c Channel) EPGID(s Stream) string {
	if c.ID == "" {
		return ""
	}
	if s.Feed != "" {
		return c.ID + "@" + s.Feed
	}
	return c.ID
}

// Country is a country that has channels.
type Country struct {
	Code  string
	Name  string
	Flag  string
	Count int
}

// Category is an iptv-org channel category.
type Category struct {
	ID   string
	Name string
}

// Catalog is a downloaded copy of the iptv-org database.
type Catalog struct {
	Channels   []Channel
	Countries  []Country
	Categories []Category
	Loaded     time.Time

	categoryNames map[string]string
	byURL         map[string][2]int // stream URL -> channel, stream index
}

// CategoryName returns the display name of a category id.
func (c *Catalog) CategoryName(id string) string {
	if n, ok := c.categoryNames[id]; ok {
		return n
	}
	return id
}

// Filter narrows a search.
type Filter struct {
	Country  string // ISO code; "" = all
	Category string // category id; "" = all
	Search   string
	Limit    int // 0 = no limit
}

// Search returns the channels matching f and whether more matched than
// the limit allowed.
func (c *Catalog) Search(f Filter) ([]Channel, bool) {
	q := strings.ToLower(strings.TrimSpace(f.Search))
	var out []Channel
	for _, ch := range c.Channels {
		if f.Country != "" && !strings.EqualFold(ch.Country, f.Country) {
			continue
		}
		if f.Category != "" && !contains(ch.Categories, f.Category) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(ch.Name), q) && !strings.Contains(strings.ToLower(ch.ID), q) {
			continue
		}
		if f.Limit > 0 && len(out) == f.Limit {
			return out, true
		}
		out = append(out, ch)
	}
	return out, false
}

// Find returns the channel and stream with the given stream URL.
func (c *Catalog) Find(streamURL string) (Channel, Stream, bool) {
	p, ok := c.byURL[streamURL]
	if !ok {
		return Channel{}, Stream{}, false
	}
	ch := c.Channels[p[0]]
	return ch, ch.Streams[p[1]], true
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Client downloads the catalog and keeps it in memory.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu      sync.Mutex
	catalog *Catalog
}

// NewClient returns a client for the public iptv-org API.
func NewClient() *Client {
	return &Client{BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

// Catalog returns the cached catalog, downloading it when there is none
// yet, it is older than maxAge or reload is set.
func (c *Client) Catalog(ctx context.Context, reload bool) (*Catalog, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.catalog != nil && !reload && time.Since(c.catalog.Loaded) < maxAge {
		return c.catalog, nil
	}
	cat, err := c.load(ctx)
	if err != nil {
		if c.catalog != nil && !reload {
			// Keep serving the old copy when iptv-org can't be reached.
			return c.catalog, nil
		}
		return nil, err
	}
	c.catalog = cat
	return cat, nil
}

type apiChannel struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Country    string   `json:"country"`
	Categories []string `json:"categories"`
	NSFW       bool     `json:"is_nsfw"`
	Closed     *string  `json:"closed"`
}

type apiStream struct {
	Channel   *string  `json:"channel"`
	Feed      *string  `json:"feed"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Quality   *string  `json:"quality"`
	Labels    []string `json:"labels"`
	UserAgent *string  `json:"user_agent"`
	Referrer  *string  `json:"referrer"`
}

type apiLogo struct {
	Channel string  `json:"channel"`
	Feed    *string `json:"feed"`
	InUse   bool    `json:"in_use"`
	URL     string  `json:"url"`
}

type apiCountry struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Flag string `json:"flag"`
}

type apiCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) load(ctx context.Context) (*Catalog, error) {
	var (
		channels   []apiChannel
		streams    []apiStream
		logos      []apiLogo
		countries  []apiCountry
		categories []apiCategory
	)
	files := []struct {
		name string
		v    any
	}{
		{"channels.json", &channels},
		{"streams.json", &streams},
		{"logos.json", &logos},
		{"countries.json", &countries},
		{"categories.json", &categories},
	}
	errs := make([]error, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.get(ctx, f.name, f.v)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return build(channels, streams, logos, countries, categories), nil
}

func (c *Client) get(ctx context.Context, name string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/"+name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "iptv-portal")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s from iptv-org: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s from iptv-org: %s", name, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("reading %s from iptv-org: %w", name, err)
	}
	return nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func build(channels []apiChannel, streams []apiStream, logos []apiLogo, countries []apiCountry, categories []apiCategory) *Catalog {
	cat := &Catalog{Loaded: time.Now(), categoryNames: map[string]string{}}
	for _, c := range categories {
		cat.Categories = append(cat.Categories, Category{ID: c.ID, Name: c.Name})
		cat.categoryNames[c.ID] = c.Name
	}
	sort.Slice(cat.Categories, func(i, j int) bool { return cat.Categories[i].Name < cat.Categories[j].Name })

	// Prefer a channel-wide logo that is in use; fall back to any logo.
	logo, feedLogo := map[string]string{}, map[string]string{}
	for _, l := range logos {
		if !l.InUse || l.URL == "" {
			continue
		}
		m := logo
		if str(l.Feed) != "" {
			m = feedLogo
		}
		if _, ok := m[l.Channel]; !ok {
			m[l.Channel] = l.URL
		}
	}
	for id, u := range feedLogo {
		if _, ok := logo[id]; !ok {
			logo[id] = u
		}
	}

	byID := map[string]apiChannel{}
	for _, c := range channels {
		byID[c.ID] = c
	}
	index := map[string]int{} // channel id -> position in cat.Channels
	for _, s := range streams {
		if s.URL == "" {
			continue
		}
		st := Stream{URL: s.URL, Title: s.Title, Feed: str(s.Feed), Quality: str(s.Quality), Labels: s.Labels,
			NeedsHeaders: str(s.UserAgent) != "" || str(s.Referrer) != ""}
		id := str(s.Channel)
		if id == "" {
			// A stream iptv-org could not link to a channel.
			cat.Channels = append(cat.Channels, Channel{Name: s.Title, Streams: []Stream{st}})
			continue
		}
		ch, ok := byID[id]
		if !ok || ch.NSFW || ch.Closed != nil {
			continue
		}
		if i, ok := index[id]; ok {
			cat.Channels[i].Streams = append(cat.Channels[i].Streams, st)
			continue
		}
		index[id] = len(cat.Channels)
		cat.Channels = append(cat.Channels, Channel{ID: id, Name: ch.Name, Country: ch.Country,
			Categories: ch.Categories, Logo: logo[id], Streams: []Stream{st}})
	}
	sort.SliceStable(cat.Channels, func(i, j int) bool {
		return strings.ToLower(cat.Channels[i].Name) < strings.ToLower(cat.Channels[j].Name)
	})
	cat.byURL = map[string][2]int{}
	for i, ch := range cat.Channels {
		for j, s := range ch.Streams {
			if _, ok := cat.byURL[s.URL]; !ok {
				cat.byURL[s.URL] = [2]int{i, j}
			}
		}
	}

	counts := map[string]int{}
	for _, ch := range cat.Channels {
		if ch.Country != "" {
			counts[ch.Country]++
		}
	}
	for _, c := range countries {
		if n := counts[c.Code]; n > 0 {
			cat.Countries = append(cat.Countries, Country{Code: c.Code, Name: c.Name, Flag: c.Flag, Count: n})
		}
	}
	sort.Slice(cat.Countries, func(i, j int) bool { return cat.Countries[i].Name < cat.Countries[j].Name })
	return cat
}
