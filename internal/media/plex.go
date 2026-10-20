package media

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// plex reads a Plex Media Server with an X-Plex-Token.
type plex struct {
	base, token string
	hc          *http.Client
}

func (p *plex) get(ctx context.Context, path string, v any) error {
	return p.getPage(ctx, path, nil, v)
}

func (p *plex) getPage(ctx context.Context, path string, h http.Header, v any) error {
	if h == nil {
		h = http.Header{}
	}
	h.Set("X-Plex-Token", p.token)
	h.Set("X-Plex-Product", "IPTV Portal")
	h.Set("X-Plex-Client-Identifier", "iptv-portal")
	return getJSON(ctx, p.hc, p.base+path, h, v)
}

func (p *plex) URL(path string) string { return p.base + withQuery(path, "X-Plex-Token", p.token) }

func (p *plex) Libraries(ctx context.Context) ([]Library, error) {
	var resp struct {
		MediaContainer struct {
			Directory []struct{ Key, Title, Type string }
		}
	}
	if err := p.get(ctx, "/library/sections", &resp); err != nil {
		return nil, err
	}
	var out []Library
	for _, d := range resp.MediaContainer.Directory {
		switch d.Type {
		case "movie":
			out = append(out, Library{ID: d.Key, Name: d.Title, Type: Movies})
		case "show":
			out = append(out, Library{ID: d.Key, Name: d.Title, Type: Shows})
		}
	}
	return out, nil
}

type plexTag struct{ Tag string }

type plexItem struct {
	RatingKey             string
	Title                 string
	Summary               string
	Year                  int
	OriginallyAvailableAt string
	Rating                float64
	AudienceRating        float64
	ContentRating         string
	Duration              int64 // ms
	AddedAt               int64
	Thumb, Art            string
	Genre                 []plexTag
	Director              []plexTag
	Role                  []plexTag
	Media                 []struct {
		Container string
		Part      []struct{ Key, Container string }
	}
	GrandparentRatingKey string
	ParentIndex          int
	Index                int
}

func (p *plex) list(ctx context.Context, lib Library, typ int) ([]plexItem, error) {
	path := "/library/sections/" + url.PathEscape(lib.ID) + "/all"
	if typ != 0 {
		path += "?type=" + strconv.Itoa(typ)
	}
	var out []plexItem
	for {
		var resp struct {
			MediaContainer struct {
				TotalSize int
				Metadata  []plexItem
			}
		}
		h := http.Header{}
		h.Set("X-Plex-Container-Start", strconv.Itoa(len(out)))
		h.Set("X-Plex-Container-Size", strconv.Itoa(pageSize))
		if err := p.getPage(ctx, path, h, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.MediaContainer.Metadata...)
		if len(resp.MediaContainer.Metadata) < pageSize ||
			(resp.MediaContainer.TotalSize > 0 && len(out) >= resp.MediaContainer.TotalSize) {
			return out, nil
		}
	}
}

func (p *plex) Items(ctx context.Context, lib Library) ([]Item, error) {
	if lib.Type == Movies {
		raw, err := p.list(ctx, lib, 0)
		if err != nil {
			return nil, err
		}
		out := make([]Item, 0, len(raw))
		for _, r := range raw {
			out = append(out, convertPlex(r, Movie))
		}
		return out, nil
	}
	shows, err := p.list(ctx, lib, 0)
	if err != nil {
		return nil, err
	}
	eps, err := p.list(ctx, lib, 4) // 4 = episodes
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(shows)+len(eps))
	for _, r := range shows {
		out = append(out, convertPlex(r, Show))
	}
	for _, r := range eps {
		out = append(out, convertPlex(r, Episode))
	}
	return out, nil
}

func tags(ts []plexTag) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Tag)
	}
	return out
}

func convertPlex(r plexItem, t ItemType) Item {
	it := Item{
		Key: r.RatingKey, Type: t, Title: r.Title, Plot: r.Summary, Year: r.Year,
		Released: r.OriginallyAvailableAt, Rating: r.AudienceRating, ContentRating: r.ContentRating,
		Duration: time.Duration(r.Duration) * time.Millisecond,
		Genres:   tags(r.Genre), Directors: tags(r.Director), Cast: tags(r.Role),
		Poster: r.Thumb, Backdrop: r.Art,
		ShowKey: r.GrandparentRatingKey, Season: r.ParentIndex, Number: r.Index,
	}
	if it.Rating == 0 {
		it.Rating = r.Rating
	}
	if len(it.Cast) > 10 {
		it.Cast = it.Cast[:10]
	}
	if r.AddedAt > 0 {
		it.Added = time.Unix(r.AddedAt, 0)
	}
	if t != Show && len(r.Media) > 0 && len(r.Media[0].Part) > 0 {
		part := r.Media[0].Part[0]
		it.Stream = part.Key
		it.Container = part.Container
		if it.Container == "" {
			it.Container = r.Media[0].Container
		}
		if it.Container == "" {
			it.Container = ext(part.Key)
		}
	}
	return it
}
