package media

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

// jellyfin reads a Jellyfin server with an API key (Dashboard > API Keys).
type jellyfin struct {
	base, token string
	hc          *http.Client

	mu     sync.Mutex
	userID string
}

func (j *jellyfin) get(ctx context.Context, p string, q url.Values, v any) error {
	u := j.base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	h := http.Header{}
	h.Set("Authorization", `MediaBrowser Client="IPTV Portal", Device="iptv-portal", DeviceId="iptv-portal", Version="`+
		version.Version+`", Token="`+j.token+`"`)
	return getJSON(ctx, j.hc, u, h, v)
}

func (j *jellyfin) URL(p string) string { return j.base + withQuery(p, "api_key", j.token) }

func (j *jellyfin) Libraries(ctx context.Context) ([]Library, error) {
	var folders []struct {
		Name           string
		ItemID         string `json:"ItemId"`
		CollectionType string
	}
	if err := j.get(ctx, "/Library/VirtualFolders", nil, &folders); err != nil {
		return nil, err
	}
	var out []Library
	for _, f := range folders {
		switch f.CollectionType {
		case "movies":
			out = append(out, Library{ID: f.ItemID, Name: f.Name, Type: Movies})
		case "tvshows":
			out = append(out, Library{ID: f.ItemID, Name: f.Name, Type: Shows})
		}
	}
	return out, nil
}

// user returns the id of an administrator. An API key has no user of its
// own, and some Jellyfin versions need one to list items.
func (j *jellyfin) user(ctx context.Context) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.userID != "" {
		return j.userID, nil
	}
	var users []struct {
		ID     string `json:"Id"`
		Policy struct{ IsAdministrator, IsDisabled bool }
	}
	if err := j.get(ctx, "/Users", nil, &users); err != nil {
		return "", err
	}
	for _, u := range users {
		if u.Policy.IsAdministrator && !u.Policy.IsDisabled {
			j.userID = u.ID
			return u.ID, nil
		}
	}
	return "", errors.New("the Jellyfin server has no administrator account")
}

type jellyfinItem struct {
	ID                string `json:"Id"`
	Name              string
	Overview          string
	ProductionYear    int
	PremiereDate      string
	CommunityRating   float64
	OfficialRating    string
	RunTimeTicks      int64
	DateCreated       string
	Container         string
	Path              string
	Genres            []string
	ImageTags         map[string]string
	BackdropImageTags []string
	People            []struct{ Name, Type string }
	ProviderIDs       map[string]string `json:"ProviderIds"`
	SeriesID          string            `json:"SeriesId"`
	ParentIndexNumber int
	IndexNumber       int
}

func (j *jellyfin) items(ctx context.Context, lib Library, kind string) ([]jellyfinItem, error) {
	uid, err := j.user(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"userId":           {uid},
		"ParentId":         {lib.ID},
		"Recursive":        {"true"},
		"IncludeItemTypes": {kind},
		"Fields":           {"Overview,Genres,DateCreated,Path,People,ProviderIds,PremiereDate"},
		"EnableImageTypes": {"Primary,Backdrop"},
	}
	var out []jellyfinItem
	for {
		q.Set("StartIndex", strconv.Itoa(len(out)))
		q.Set("Limit", strconv.Itoa(pageSize))
		var resp struct {
			Items            []jellyfinItem
			TotalRecordCount int
		}
		if err := j.get(ctx, "/Items", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Items...)
		if len(resp.Items) < pageSize || (resp.TotalRecordCount > 0 && len(out) >= resp.TotalRecordCount) {
			return out, nil
		}
	}
}

func (j *jellyfin) Items(ctx context.Context, lib Library) ([]Item, error) {
	if lib.Type == Movies {
		raw, err := j.items(ctx, lib, "Movie")
		if err != nil {
			return nil, err
		}
		out := make([]Item, 0, len(raw))
		for _, r := range raw {
			out = append(out, j.convert(r, Movie))
		}
		return out, nil
	}
	shows, err := j.items(ctx, lib, "Series")
	if err != nil {
		return nil, err
	}
	eps, err := j.items(ctx, lib, "Episode")
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(shows)+len(eps))
	for _, r := range shows {
		out = append(out, j.convert(r, Show))
	}
	for _, r := range eps {
		out = append(out, j.convert(r, Episode))
	}
	return out, nil
}

func (j *jellyfin) convert(r jellyfinItem, t ItemType) Item {
	it := Item{
		Key: r.ID, Type: t, Title: r.Name, Plot: r.Overview, Year: r.ProductionYear,
		Genres: r.Genres, Rating: r.CommunityRating, ContentRating: r.OfficialRating,
		Duration:  time.Duration(r.RunTimeTicks) * 100, // ticks are 100 ns
		TMDBID:    r.ProviderIDs["Tmdb"],
		ShowKey:   r.SeriesID,
		Season:    r.ParentIndexNumber,
		Number:    r.IndexNumber,
		Container: ext(r.Path),
	}
	if len(r.PremiereDate) >= 10 {
		it.Released = r.PremiereDate[:10]
	}
	if t, err := time.Parse(time.RFC3339, r.DateCreated); err == nil {
		it.Added = t
	}
	if it.Container == "" {
		it.Container, _, _ = strings.Cut(r.Container, ",")
	}
	for _, p := range r.People {
		switch p.Type {
		case "Director":
			it.Directors = append(it.Directors, p.Name)
		case "Actor":
			if len(it.Cast) < 10 {
				it.Cast = append(it.Cast, p.Name)
			}
		}
	}
	if _, ok := r.ImageTags["Primary"]; ok {
		it.Poster = "/Items/" + r.ID + "/Images/Primary?maxWidth=600"
	}
	if len(r.BackdropImageTags) > 0 {
		it.Backdrop = "/Items/" + r.ID + "/Images/Backdrop/0?maxWidth=1280"
	}
	if t != Show {
		// static=true sends the original file, which players handle best
		// and costs the server no transcoding.
		it.Stream = "/Videos/" + r.ID + "/stream?static=true"
	}
	return it
}
