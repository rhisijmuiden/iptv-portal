package media

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Category is a genre or library, shown to players as a VOD or series
// category.
type Category struct {
	ID       int64
	ServerID int64
	Name     string
	Type     LibraryType
	// Other is the category of items without a genre.
	Other bool
	// Items is the number of movies or series in the category.
	Items int
}

// OtherName is the name of the category of items without a genre.
const OtherName = "Other"

// libraryGenreSep separates the library and genre in category names like
// "Films | Action".
const libraryGenreSep = " | "

// ServerStatus describes the last refresh of one server.
type ServerStatus struct {
	UpdatedAt time.Time
	Movies    int
	Shows     int
	Episodes  int
	// Categories is the number of movie and series categories.
	Categories int
	Error      string
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
	c := &Catalog{store: st, log: log, hc: NewHTTPClient(2 * time.Minute), newClient: NewClient,
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
			st.Categories = len(snap.categories)
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
			return nil, fmt.Errorf("reading library %q: %w", l.Name, err)
		}
		offered = append(offered, l)
		libItems = append(libItems, items)
	}

	// Every movie and series gets the categories of the server's
	// grouping; catKeys holds them per library, in item order.
	catKeys := make([][][]catRef, len(offered))
	for i, l := range offered {
		catKeys[i] = make([][]catRef, len(libItems[i]))
		for j, it := range libItems[i] {
			if it.Type != Episode {
				catKeys[i][j] = categoryRefs(s.Categories, l, it.Genres)
			}
		}
	}

	// Numeric ids for categories and items.
	keys := []string{}
	for i := range offered {
		for j, it := range libItems[i] {
			keys = append(keys, itemKey(it.Type, it.Key))
			for _, ref := range catKeys[i][j] {
				keys = append(keys, ref.key)
			}
		}
	}
	ids, err := c.store.MediaIDs(ctx, s.ID, keys)
	if err != nil {
		return nil, err
	}
	cats := map[int64]*Category{}
	var order []int64
	for i, l := range offered {
		shows := map[string]int64{}
		for _, it := range libItems[i] {
			if it.Type == Show {
				shows[it.Key] = ids[itemKey(Show, it.Key)]
			}
		}
		for j, it := range libItems[i] {
			it := it
			it.ID = ids[itemKey(it.Type, it.Key)]
			it.ServerID = s.ID
			if it.Type == Episode {
				if it.ShowID = shows[it.ShowKey]; it.ShowID == 0 {
					continue // episode of a series outside this library
				}
			}
			if it.Type != Show && it.Stream == "" {
				continue // nothing to play
			}
			for _, ref := range catKeys[i][j] {
				id := ids[ref.key]
				cat := cats[id]
				if cat == nil {
					cat = &Category{ID: id, ServerID: s.ID, Name: ref.name, Type: l.Type,
						Other: strings.HasPrefix(ref.key, "other:")}
					cats[id] = cat
					order = append(order, id)
				}
				cat.Items++
				it.CategoryIDs = append(it.CategoryIDs, id)
			}
			snap.items = append(snap.items, &it)
		}
	}
	for _, id := range order {
		snap.categories = append(snap.categories, *cats[id])
	}
	return snap, nil
}

// catRef is a category of an item: its key in the media id table and its
// name.
type catRef struct{ key, name string }

// categoryRefs returns the category keys (unique per server) and names of
// a movie or series in library l.
func categoryRefs(mode string, l Library, genres []string) []catRef {
	type ref = catRef
	if mode == store.CategoriesLibraries {
		return []ref{{"library:" + l.ID, l.Name}}
	}
	scope, prefix := string(l.Type), ""
	if mode == store.CategoriesLibraryGenres {
		scope, prefix = l.ID, l.Name+libraryGenreSep
	}
	var out []ref
	seen := map[string]bool{}
	for _, g := range genres {
		g = strings.Join(strings.Fields(g), " ")
		k := strings.ToLower(g)
		if g == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ref{"genre:" + scope + ":" + k, prefix + g})
	}
	if len(out) == 0 {
		out = append(out, ref{"other:" + scope, prefix + OtherName})
	}
	return out
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
	// Categories with the same name on several servers become one; the
	// first server's id is kept.
	type catName struct {
		t    LibraryType
		name string
	}
	merged := map[catName]int{} // index in c.categories
	remap := map[int64]int64{}
	for _, id := range ids {
		for _, cat := range c.snaps[id].categories {
			k := catName{cat.Type, strings.ToLower(cat.Name)}
			if i, ok := merged[k]; ok {
				remap[cat.ID] = c.categories[i].ID
				c.categories[i].Items += cat.Items
				continue
			}
			merged[k] = len(c.categories)
			c.categories = append(c.categories, cat)
		}
	}
	slices.SortStableFunc(c.categories, compareCategories)
	for _, id := range ids {
		snap := c.snaps[id]
		c.clients[id] = snap.client
		c.servers[id] = snap.server
		for _, it := range snap.items {
			if len(remap) > 0 && len(it.CategoryIDs) > 0 {
				// Copy, so the snapshot keeps its own ids for the next
				// rebuild.
				cp := *it
				cp.CategoryIDs = nil
				for _, cid := range it.CategoryIDs {
					if to, ok := remap[cid]; ok {
						cid = to
					}
					if !slices.Contains(cp.CategoryIDs, cid) {
						cp.CategoryIDs = append(cp.CategoryIDs, cid)
					}
				}
				it = &cp
			}
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

// Categories returns the categories of the given type: genres or
// libraries, by name, with "Other" last.
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

// ServerCategories returns the categories read from one server, movies
// first, each by name with "Other" last.
func (c *Catalog) ServerCategories(serverID int64) []Category {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap := c.snaps[serverID]
	if snap == nil {
		return nil
	}
	out := slices.Clone(snap.categories)
	slices.SortStableFunc(out, func(a, b Category) int {
		return cmp.Or(cmp.Compare(a.Type, b.Type), compareCategories(a, b)) // "movies" < "shows"
	})
	return out
}

// compareCategories orders categories by name, with "Other" last.
func compareCategories(a, b Category) int {
	if a.Other != b.Other {
		if a.Other {
			return 1
		}
		return -1
	}
	return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
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

// WebURL returns the full address (with token) that plays a movie or
// episode in a web browser, and the token as query parameter for the
// addresses in the server's HLS playlists. The browser always gets it
// through the portal, so the token stays on the server.
func (c *Catalog) WebURL(it *Item) (u string, auth url.Values) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cl, ok := c.clients[it.ServerID]
	if !ok {
		return "", nil
	}
	p := cl.WebPath(it)
	if p == "" {
		return "", nil
	}
	return cl.URL(p), cl.Auth()
}
