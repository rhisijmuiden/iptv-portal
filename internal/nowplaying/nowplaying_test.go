package nowplaying

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// icyBody is metaint bytes of "audio" followed by a metadata block.
func icyBody(metaint int, meta string) []byte {
	b := make([]byte, metaint)
	n := (len(meta) + 15) / 16
	b = append(b, byte(n))
	block := make([]byte, n*16)
	copy(block, meta)
	return append(b, block...)
}

func icyServer(t *testing.T, meta string, reads *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reads != nil {
			reads.Add(1)
		}
		if r.Header.Get("Icy-MetaData") != "1" {
			t.Errorf("Icy-MetaData header missing")
		}
		w.Header().Set("icy-metaint", "8192")
		w.Header().Set("icy-name", "Radio Test")
		w.Header().Set("icy-genre", "Pop")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(icyBody(8192, meta))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReadICY(t *testing.T) {
	srv := icyServer(t, "StreamTitle='Doe Maar - De Bom';StreamUrl='';", nil)
	info, err := Read(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Doe Maar - De Bom" || info.Artist != "Doe Maar" || info.Song != "De Bom" ||
		info.Station != "Radio Test" || info.Genre != "Pop" {
		t.Fatalf("info = %+v", info)
	}
}

func TestReadICYLatin1AndQuotes(t *testing.T) {
	srv := icyServer(t, "StreamTitle='Andr\xe9 Hazes - Zij gelooft in mij (live) ''n';", nil)
	info, err := Read(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if info.Artist != "André Hazes" || info.Song != "Zij gelooft in mij (live) ''n" {
		t.Fatalf("info = %+v", info)
	}
}

func TestReadWithoutMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/aac")
		w.Header().Set("icy-name", "Quiet FM")
		w.Write(make([]byte, 1024))
	}))
	defer srv.Close()
	info, err := Read(context.Background(), http.DefaultClient, srv.URL)
	if !errors.Is(err, ErrNoMetadata) || info.Station != "Quiet FM" {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
	// An empty title is no title either.
	srv2 := icyServer(t, "StreamTitle=' - ';", nil)
	if _, err := Read(context.Background(), http.DefaultClient, srv2.URL); !errors.Is(err, ErrNoMetadata) {
		t.Fatalf("empty title: err = %v", err)
	}
}

func TestReadHLS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=128000\nmedia/audio.m3u8\n")
	})
	mux.HandleFunc("/media/audio.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10.0,title=\"Old\",artist=\"Song\"\nseg1.aac\n"+
			"#EXTINF:10.0,title=\"Bohemian Rhapsody\",artist=\"Queen\"\nseg2.aac\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	info, err := Read(context.Background(), http.DefaultClient, srv.URL+"/master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Queen - Bohemian Rhapsody" || info.Artist != "Queen" {
		t.Fatalf("info = %+v", info)
	}
}

// TestShoutcastStatusLine checks that an "ICY 200 OK" answer is read.
func TestShoutcastStatusLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		for {
			l, err := br.ReadString('\n')
			if err != nil || strings.TrimSpace(l) == "" {
				break
			}
		}
		fmt.Fprint(c, "ICY 200 OK\r\nicy-name:Old School\r\nicy-metaint:1000\r\ncontent-type:audio/mpeg\r\n\r\n")
		c.Write(icyBody(1000, "StreamTitle='Golden Earring - Radar Love';"))
	}()
	s := New()
	info := s.Get(context.Background(), 1, "http://"+ln.Addr().String()+"/")
	if info.Err != "" || info.Title != "Golden Earring - Radar Love" || info.Station != "Old School" {
		t.Fatalf("info = %+v", info)
	}
}

func TestServiceCaches(t *testing.T) {
	var reads atomic.Int32
	srv := icyServer(t, "StreamTitle='A - B';", &reads)
	s := New()
	now := time.Now()
	s.now = func() time.Time { return now }
	first := s.Get(context.Background(), 7, srv.URL)
	s.Get(context.Background(), 7, srv.URL)
	if reads.Load() != 1 {
		t.Fatalf("reads = %d, want 1 within the TTL", reads.Load())
	}
	now = now.Add(TTL + time.Second)
	again := s.Get(context.Background(), 7, srv.URL)
	if reads.Load() != 2 {
		t.Fatalf("reads = %d, want 2 after the TTL", reads.Load())
	}
	if !again.Since.Equal(first.Since) {
		t.Fatalf("Since moved for the same song: %v -> %v", first.Since, again.Since)
	}
	if c, ok := s.Cached(7); !ok || c.Title != "A - B" {
		t.Fatalf("Cached = %+v, %v", c, ok)
	}
}
