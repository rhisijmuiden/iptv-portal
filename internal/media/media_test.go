package media

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// fakeJellyfin answers the Jellyfin endpoints the client uses.
func fakeJellyfin(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="jf-key"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Library/VirtualFolders":
			w.Write([]byte(`[{"Name":"Films","ItemId":"lib-m","CollectionType":"movies"},
				{"Name":"Shows","ItemId":"lib-s","CollectionType":"tvshows"},
				{"Name":"Music","ItemId":"lib-x","CollectionType":"music"}]`))
		case "/Users":
			w.Write([]byte(`[{"Id":"u1","Policy":{"IsAdministrator":false}},{"Id":"u2","Policy":{"IsAdministrator":true}}]`))
		case "/Items":
			if r.URL.Query().Get("userId") != "u2" {
				t.Errorf("userId = %q", r.URL.Query().Get("userId"))
			}
			switch r.URL.Query().Get("IncludeItemTypes") {
			case "Movie":
				w.Write([]byte(`{"Items":[{"Id":"m1","Name":"Zorro","Overview":"Sword","ProductionYear":1998,
					"CommunityRating":6.7,"RunTimeTicks":82800000000,"DateCreated":"2024-05-01T10:00:00.0000000Z",
					"Path":"/media/zorro.MKV","Container":"mkv","Genres":["Action"],"ImageTags":{"Primary":"x"},
					"BackdropImageTags":["y"],"People":[{"Name":"Antonio","Type":"Actor"},{"Name":"Martin","Type":"Director"}],
					"ProviderIds":{"Tmdb":"9342"}},
					{"Id":"m2","Name":"Amelie","ProductionYear":2001,"Container":"mov,mp4,m4a"}]}`))
			case "Series":
				w.Write([]byte(`{"Items":[{"Id":"s1","Name":"Show","ImageTags":{"Primary":"x"}}]}`))
			case "Episode":
				w.Write([]byte(`{"Items":[{"Id":"e2","Name":"Two","SeriesId":"s1","ParentIndexNumber":1,"IndexNumber":2,"Path":"/e2.mp4"},
					{"Id":"e1","Name":"One","SeriesId":"s1","ParentIndexNumber":1,"IndexNumber":1,"Path":"/e1.mp4"},
					{"Id":"e9","Name":"Orphan","SeriesId":"elsewhere","Path":"/e9.mp4"}]}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakePlex(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != "plex-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/library/sections":
			w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"1","title":"Movies","type":"movie"},
				{"key":"2","title":"TV","type":"show"},{"key":"3","title":"Music","type":"artist"}]}}`))
		case "/library/sections/1/all":
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"10","title":"Heat","year":1995,"summary":"Bank",
				"audienceRating":8.3,"duration":10200000,"addedAt":1700000000,"thumb":"/library/metadata/10/thumb/1",
				"art":"/library/metadata/10/art/1","Genre":[{"tag":"Crime"}],"Role":[{"tag":"Al"}],
				"Media":[{"container":"mkv","Part":[{"key":"/library/parts/55/1/file.mkv","container":"mkv"}]}]}]}}`))
		case "/library/sections/2/all":
			if r.URL.Query().Get("type") == "4" {
				w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"21","title":"Pilot","grandparentRatingKey":"20",
					"parentIndex":1,"index":1,"Media":[{"container":"mp4","Part":[{"key":"/library/parts/66/1/file.mp4"}]}]}]}}`))
				return
			}
			w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"20","title":"Series","thumb":"/library/metadata/20/thumb/1"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestJellyfinClient(t *testing.T) {
	srv := fakeJellyfin(t)
	cl, _ := NewClient(store.MediaServer{Kind: store.MediaJellyfin, URL: srv.URL + "/", Token: "jf-key"}, nil)
	ctx := context.Background()
	libs, err := cl.Libraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 || libs[0] != (Library{ID: "lib-m", Name: "Films", Type: Movies}) || libs[1].Type != Shows {
		t.Fatalf("libraries = %+v", libs)
	}
	movies, err := cl.Items(ctx, libs[0])
	if err != nil {
		t.Fatal(err)
	}
	m := movies[0]
	if m.Title != "Zorro" || m.Year != 1998 || m.Duration != 2*time.Hour+18*time.Minute || m.Container != "mkv" ||
		m.TMDBID != "9342" || len(m.Cast) != 1 || len(m.Directors) != 1 || m.Added.Year() != 2024 {
		t.Fatalf("movie = %+v", m)
	}
	if got := cl.URL(m.Stream); got != srv.URL+"/Videos/m1/stream?static=true&api_key=jf-key" {
		t.Errorf("stream URL = %s", got)
	}
	if m.Poster == "" || m.Backdrop == "" {
		t.Errorf("images missing: %+v", m)
	}
	if movies[1].Container != "mov" || movies[1].Poster != "" {
		t.Errorf("second movie = %+v", movies[1])
	}

	bad, _ := NewClient(store.MediaServer{Kind: store.MediaJellyfin, URL: srv.URL, Token: "wrong"}, nil)
	if _, err := bad.Libraries(ctx); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("wrong token: %v", err)
	}
}

func TestPlexClient(t *testing.T) {
	srv := fakePlex(t)
	cl, _ := NewClient(store.MediaServer{Kind: store.MediaPlex, URL: srv.URL, Token: "plex-tok"}, nil)
	ctx := context.Background()
	libs, err := cl.Libraries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 {
		t.Fatalf("libraries = %+v", libs)
	}
	movies, err := cl.Items(ctx, libs[0])
	if err != nil {
		t.Fatal(err)
	}
	m := movies[0]
	if m.Title != "Heat" || m.Rating != 8.3 || m.Duration != 170*time.Minute || m.Container != "mkv" ||
		m.Genres[0] != "Crime" || m.Added.Unix() != 1700000000 {
		t.Fatalf("movie = %+v", m)
	}
	if got := cl.URL(m.Stream); got != srv.URL+"/library/parts/55/1/file.mkv?X-Plex-Token=plex-tok" {
		t.Errorf("stream URL = %s", got)
	}
	shows, err := cl.Items(ctx, libs[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(shows) != 2 || shows[0].Type != Show || shows[1].Type != Episode || shows[1].ShowKey != "20" ||
		shows[1].Container != "mp4" {
		t.Fatalf("shows = %+v", shows)
	}
}

func TestCatalog(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	jf := fakeJellyfin(t)
	id, err := st.SaveMediaServer(ctx, store.MediaServer{Name: "Home", Kind: store.MediaJellyfin, URL: jf.URL,
		Token: "jf-key", Libraries: []string{"lib-m", "lib-s"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	c := NewCatalog(st, slog.New(slog.DiscardHandler))
	c.Refresh(ctx)

	if s := c.Status(id); s.Error != "" || s.Movies != 2 || s.Shows != 1 || s.Episodes != 2 {
		t.Fatalf("status = %+v", s)
	}
	movies := c.Movies()
	if len(movies) != 2 || movies[0].Title != "Amelie" {
		t.Fatalf("movies not sorted: %v", movies)
	}
	cats := c.Categories(Movies)
	if len(cats) != 1 || cats[0].Name != "Films" || movies[0].CategoryID != cats[0].ID {
		t.Fatalf("categories = %+v", cats)
	}
	show := c.Shows()[0]
	eps := c.Episodes(show.ID)
	if len(eps) != 2 || eps[0].Title != "One" {
		t.Fatalf("episodes = %+v", eps)
	}
	if _, ok := c.Item(show.ID, Movie); ok {
		t.Error("a series was returned as a movie")
	}

	// Ids stay the same after a refresh.
	zorro := movies[1].ID
	c.Refresh(ctx)
	if it, ok := c.Item(zorro, Movie); !ok || it.Title != "Zorro" {
		t.Fatalf("id changed after refresh")
	}

	// A server that stops answering keeps its items.
	s, _ := st.MediaServer(ctx, id)
	s.Token = "wrong"
	st.SaveMediaServer(ctx, s)
	c.Refresh(ctx)
	if st := c.Status(id); st.Error == "" || len(c.Movies()) != 2 {
		t.Fatalf("failed refresh: status %+v, %d movies", st, len(c.Movies()))
	}

	// Only offered libraries are read; removed servers disappear.
	s.Token, s.Libraries = "jf-key", []string{"lib-s"}
	st.SaveMediaServer(ctx, s)
	c.Refresh(ctx)
	if len(c.Movies()) != 0 || len(c.Shows()) != 1 {
		t.Fatalf("after unticking movies: %d movies, %d shows", len(c.Movies()), len(c.Shows()))
	}
	st.DeleteMediaServer(ctx, id)
	c.Refresh(ctx)
	if len(c.Shows()) != 0 || len(c.Categories(Shows)) != 0 {
		t.Fatal("deleted server still offered")
	}
}
