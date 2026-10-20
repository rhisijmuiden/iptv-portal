package health

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

func streamServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/live/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nlow/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=2000000\nhigh/index.m3u8\n"))
	})
	mux.HandleFunc("/live/low/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("\xef\xbb\xbf#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg1.ts\n#EXTINF:6,\n/live/low/seg2.ts\n"))
	})
	mux.HandleFunc("/live/low/seg2.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 188*10))
	})
	mux.HandleFunc("/expired.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXTINF:6,\ngone.ts\n"))
	})
	mux.HandleFunc("/html.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>Geo-blocked</html>"))
	})
	mux.HandleFunc("/empty.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n"))
	})
	mux.HandleFunc("/stream.ts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(make([]byte, 188))
	})
	mux.HandleFunc("/moved.m3u8", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/live/low/index.m3u8", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestProbe(t *testing.T) {
	srv := streamServer(t)
	c := New(nil, slog.New(slog.DiscardHandler))
	for _, tc := range []struct{ path, ok, err string }{
		{"/live/master.m3u8", "HLS, 2 variants, segments load", ""},
		{"/moved.m3u8", "HLS, segments load", ""},
		{"/stream.ts", "stream responds", ""},
		{"/missing.m3u8", "", "HTTP 404 Not Found"},
		{"/expired.m3u8", "", "segment: HTTP 404 Not Found"},
		{"/html.m3u8", "", "the response is not an HLS playlist"},
		{"/empty.m3u8", "", "the master playlist lists no streams"},
	} {
		msg, err := c.Probe(context.Background(), srv.URL+tc.path)
		switch {
		case tc.err == "" && (err != nil || msg != tc.ok):
			t.Errorf("%s: %q, %v; want %q", tc.path, msg, err, tc.ok)
		case tc.err != "" && (err == nil || err.Error() != tc.err):
			t.Errorf("%s: %q, %v; want error %q", tc.path, msg, err, tc.err)
		}
	}
	if _, err := c.Probe(context.Background(), "http://127.0.0.1:1/x.m3u8"); err == nil || err.Error() != "connection refused" {
		t.Errorf("closed port: %v", err)
	}
}

func TestCheckAllAutoDisable(t *testing.T) {
	srv := streamServer(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SaveHealthSettings(ctx, store.HealthSettings{IntervalMinutes: 60, DisableAfter: 2}); err != nil {
		t.Fatal(err)
	}
	good, _ := st.SaveChannel(ctx, store.Channel{Name: "Good", StreamURL: srv.URL + "/live/master.m3u8", Enabled: true})
	bad, _ := st.SaveChannel(ctx, store.Channel{Name: "Bad", StreamURL: srv.URL + "/missing.m3u8", Enabled: true})
	c := New(st, slog.New(slog.DiscardHandler))

	enabled := func(id int64) bool {
		ch, err := st.Channel(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return ch.Enabled
	}
	c.CheckAll(ctx)
	if !enabled(bad) {
		t.Fatal("switched off after one failure")
	}
	c.CheckAll(ctx)
	all, err := st.ChannelHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h := all[good]; !h.OK || h.Failures != 0 || h.LastOKAt == nil {
		t.Errorf("good channel: %+v", h)
	}
	if h := all[bad]; h.OK || h.Failures != 2 || !h.AutoDisabled || h.Message != "HTTP 404 Not Found" {
		t.Errorf("bad channel: %+v", h)
	}
	if enabled(bad) || !enabled(good) {
		t.Error("bad channel should be switched off, good one on")
	}
	if s := c.Status(); s.Running || s.Total != 2 || s.Done != 2 || s.LastRun.IsZero() {
		t.Errorf("status %+v", s)
	}

	// Once the stream works again the channel comes back.
	bc, _ := st.Channel(ctx, bad)
	bc.StreamURL = srv.URL + "/stream.ts"
	if _, err := st.SaveChannel(ctx, bc); err != nil {
		t.Fatal(err)
	}
	h, err := c.CheckChannel(ctx, bc)
	if err != nil || !h.OK || h.AutoDisabled {
		t.Fatalf("recheck: %+v %v", h, err)
	}
	if !enabled(bad) {
		t.Error("channel was not switched back on")
	}

	// A channel the admin switched off is never switched on by the checker.
	if err := st.SetChannelEnabled(ctx, good, false); err != nil {
		t.Fatal(err)
	}
	c.CheckAll(ctx)
	if enabled(good) {
		t.Error("checker switched on a channel the admin had switched off")
	}
}
