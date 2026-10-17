package media

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Category is an offered library, shown to players as a VOD or series
// category.
type Category struct {
	ID       int64
	ServerID int64
	Name     string
	Type     LibraryType
}

// ServerStatus describes the last refresh of one server.
type ServerStatus struct {
	UpdatedAt time.Time
	Movies    int
	Shows     int
	Episodes  int
	Error     string
}

// snapshot is everything read from one server.
type snapshot struct {
	server     store.MediaServer
	client     Client
	categories []Category
	items      []*Item
}

// Catalog keeps the offered movies and series of all media servers in
// memory and refreshes them periodically.
type Catalog struct {
	store *store.Store
	log   *slog.Logger
	hc    *http.Client
	// newClient is replaced in tests.
	newClient func(store.MediaServer, *http.Client) (Client, error)

	mu         sync.RWMutex
	snaps      map[int64]*snapshot
	status     map[int64]ServerStatus
	refreshing bool

	// built from snaps
	categories []Category
	movies     []*Item
	shows      []*Item
	byID       map[int64]*Item
	episodes   map[int64][]*Item // by show id
	clients    map[int64]Client
	servers    map[int64]store.MediaServer
}

// NewCatalog creates an empty catalog; call Refresh or Loop to fill it.
func NewCatalog(st *store.Store, log *slog.Logger) *Catalog {
	c := &Catalog{store: st, log: log, hc: &http.Client{Timeout: 5 * time.Minute}, newClient: NewClient,
		snaps: map[int64]*snapshot{}, status: map[int64]ServerStatus{}}
	c.rebuild()
	return c
}

// Loop refreshes the catalog now, every interval and whenever refresh
// receives a value, until ctx is done.
func (c *Catalog) Loop(ctx context.Context, interval time.Duration, refresh <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		c.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-refresh:
		}
	}
}

// Refreshing reports whether a refresh is running.
func (c *Catalog) Refreshing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.refreshing
}

// Status returns the last refresh result of a server.
func (c *Catalog) Status(serverID int64) ServerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status[serverID]
}

// Refresh reads all enabled servers again. A server that fails keeps the
// items of its last successful refresh.
func (c *Catalog) Refresh(ctx context.Context) {
	c.mu.Lock()
	c.refreshing = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.refreshing = false
		c.mu.Unlock()
	}()

	servers, err := c.store.MediaServers(ctx)
	if err != nil {
		c.log.Warn("reading media servers", "err", err)
		return
	}
	keep := map[int64]bool{}
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		keep[s.ID] = true
		snap, err := c.load(ctx, s)
		c.mu.Lock()
		st := c.status[s.ID]
		if err != nil {
			c.log.Warn("refreshing media server", "server", s.Name, "err", err)
			st.Error = err.Error()
			if old := c.snaps[s.ID]; old != nil {
				// Keep the old items, but use the new settings.
				old.server = s
				if cl, err := c.newClient(s, c.hc); err == nil {
					old.client = cl
				}
			}
		} else {
			c.snaps[s.ID] = snap
			st = ServerStatus{UpdatedAt: time.Now()}
			for _, it := range snap.items {
				switch it.Type {
				case Movie:
					st.Movies++
				case Show:
					st.Shows++
				case Episode:
					st.Episodes++
				}
			}
		}
		c.status[s.ID] = st
		c.mu.Unlock()
	}
	c.mu.Lock()
	for id := range c.snaps {
		if !keep[id] {
			delete(c.snaps, id)
			delete(c.status, id)
		}
	}
	c.mu.Unlock()
	c.rebuild()
}

// load reads the offered libraries of one server.
func (c *Catalog) load(ctx context.Context, s store.MediaServer) (*snapshot, error) {
	client, err := c.newClient(s, c.hc)
	if err != nil {
		return nil, err
	}
	libs, err := client.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	snap := &snapshot{server: s, client: client}
	var libItems [][]Item
	var offered []Library
	for _, l := range libs {
		if !s.HasLibrary(l.ID) {
			continue
		}
		items, err := client.Items(ctx, l)
		if err != nil {
			return nil, err
		}
		offered = append(offered, l)
		libItems = append(libItems, items)
	}

	// Numeric ids for libraries and items.
	keys := []string{}
	for i, l := range offered {
		keys = append(keys, "library:"+l.ID)
		for _, it := range libItems[i] {
			keys = append(keys, itemKey(it.Type, it.Key))
		}
	}
	ids, err := c.store.MediaIDs(ctx, s.ID, keys)
	if err != nil {
		return nil, err
	}
	for i, l := range offered {
		catID := ids["library:"+l.ID]
		snap.categories = append(snap.categories, Category{ID: catID, ServerID: s.ID, Name: l.Name, Type: l.Type})
		shows := map[string]int64{}
		for _, it := range libItems[i] {
			if it.Type == Show {
				shows[it.Key] = ids[itemKey(Show, it.Key)]
			}
		}
		for _, it := range libItems[i] {
			it := it
			it.ID = ids[itemKey(it.Type, it.Key)]
			it.ServerID = s.ID
			it.CategoryID = catID
			if it.Type == Episode {
				if it.ShowID = shows[it.ShowKey]; it.ShowID == 0 {
					continue // episode of a series outside this library
				}
			}
			if it.Type != Show && it.Stream == "" {
				continue // nothing to play
			}
			snap.items = append(snap.items, &it)
		}
	}
	return snap, nil
}

func itemKey(t ItemType, key string) string {
	switch t {
	case Movie:
		return "movie:" + key
	case Show:
		return "show:" + key
	}
	return "episode:" + key
}

// rebuild recreates the lookup tables from the snapshots.
func (c *Catalog) rebuild() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.categories, c.movies, c.shows = nil, nil, nil
	c.byID = map[int64]*Item{}
	c.episodes = map[int64][]*Item{}
	c.clients = map[int64]Client{}
	c.servers = map[int64]store.MediaServer{}
	ids := make([]int64, 0, len(c.snaps))
	for id := range c.snaps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		snap := c.snaps[id]
		c.clients[id] = snap.client
		c.servers[id] = snap.server
		c.categories = append(c.categories, snap.categories...)
		for _, it := range snap.items {
			c.byID[it.ID] = it
			switch it.Type {
			case Movie:
				c.movies = append(c.movies, it)
			case Show:
				c.shows = append(c.shows, it)
			case Episode:
				c.episodes[it.ShowID] = append(c.episodes[it.ShowID], it)
			}
		}
	}
	byTitle := func(a, b *Item) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)), cmp.Compare(a.Year, b.Year))
	}
	slices.SortFunc(c.movies, byTitle)
	slices.SortFunc(c.shows, byTitle)
	for _, eps := range c.episodes {
		slices.SortFunc(eps, func(a, b *Item) int {
			return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Number, b.Number), cmp.Compare(a.Title, b.Title))
		})
	}
}

// Categories returns the offered libraries of the given type.
func (c *Catalog) Categories(t LibraryType) []Category {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Category
	for _, cat := range c.categories {
		if cat.Type == t {
			out = append(out, cat)
		}
	}
	return out
}

// Movies returns all movies, sorted by title.
func (c *Catalog) Movies() []*Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.movies
}

// Shows returns all series, sorted by title.
func (c *Catalog) Shows() []*Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.shows
}

// Item returns the item with the given portal id and type.
func (c *Catalog) Item(id int64, t ItemType) (*Item, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	it, ok := c.byID[id]
	if !ok || it.Type != t {
		return nil, false
	}
	return it, true
}

// Episodes returns the episodes of a series, by season and number.
func (c *Catalog) Episodes(showID int64) []*Item {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.episodes[showID]
}

// URL returns the full address (with token) of a path on the item's server,
// and whether players may be sent there directly.
func (c *Catalog) URL(it *Item, path string) (u string, direct bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cl, ok := c.clients[it.ServerID]
	if !ok || path == "" {
		return "", false
	}
	return cl.URL(path), c.servers[it.ServerID].Direct
}
