// Package nowplaying reads what a radio station is playing from its stream:
// the ICY metadata (StreamTitle) that Icecast and Shoutcast servers send
// between the audio, or the title of the last segment of an HLS playlist.
// Results are cached for a short while, so players that ask for the guide
// of many stations at once do not open a stream for each request.
package nowplaying

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

const (
	// TTL is how long a result is reused.
	TTL = 30 * time.Second
	// timeout limits reading one stream.
	timeout = 8 * time.Second
	// maxMetaInt is the largest audio block accepted before the metadata;
	// servers use 8 KB to 32 KB.
	maxMetaInt = 256 << 10
	maxBody    = 1 << 20
)

// Info is what a station reports about itself and the current song.
type Info struct {
	// Title is the full stream title, usually "Artist - Song". Empty when
	// the station does not send one.
	Title string
	// Artist and Song are Title split at " - "; Song is the whole title
	// when it has no separator.
	Artist, Song string
	// Station, Genre and Description come from the icy-* headers.
	Station, Genre, Description string
	// Since is when Title was first seen.
	Since time.Time
	// Checked is when the stream was read; Err is set when that failed.
	Checked time.Time
	Err     string
}

// ErrNoMetadata means the stream works but does not say what is playing.
var ErrNoMetadata = errors.New("the stream does not send what is playing")

// Service reads and caches now playing information per channel.
type Service struct {
	client *http.Client
	now    func() time.Time

	mu    sync.Mutex
	cache map[int64]*entry
}

type entry struct {
	url  string
	info Info
	done chan struct{} // closed when a read in progress finishes
}

// New returns a service with an empty cache.
func New() *Service {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dial := (&net.Dialer{Timeout: timeout}).DialContext
	// Shoutcast 1 answers "ICY 200 OK" instead of "HTTP/1.0 200 OK", which
	// net/http does not accept; rewrite it on plain HTTP connections.
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &icyConn{Conn: c}, nil
	}
	return &Service{client: &http.Client{Transport: tr, Timeout: timeout}, now: time.Now, cache: map[int64]*entry{}}
}

// Cached returns the last result for a channel, also when it is older
// than TTL, without reading the stream.
func (s *Service) Cached(id int64) (Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.cache[id]
	if e == nil || e.info.Checked.IsZero() {
		return Info{}, false
	}
	return e.info, true
}

// Get returns what channel id plays, reading streamURL when the cached
// result is older than TTL. Concurrent calls for one channel share a read.
func (s *Service) Get(ctx context.Context, id int64, streamURL string) Info {
	s.mu.Lock()
	e := s.cache[id]
	if e != nil && e.url == streamURL {
		if e.done != nil {
			done := e.done
			s.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
			}
			s.mu.Lock()
			info := e.info
			s.mu.Unlock()
			return info
		}
		if s.now().Sub(e.info.Checked) < TTL {
			info := e.info
			s.mu.Unlock()
			return info
		}
	}
	if e == nil || e.url != streamURL {
		e = &entry{url: streamURL}
		s.cache[id] = e
	}
	done := make(chan struct{})
	e.done = done
	prev := e.info
	s.mu.Unlock()

	// The read is not tied to ctx, so a player that hangs up does not
	// spoil the result for the next request.
	rctx, cancel := context.WithTimeout(context.Background(), timeout)
	info, err := Read(rctx, s.client, streamURL)
	cancel()
	info.Checked = s.now()
	if err != nil {
		info.Err = err.Error()
	}
	info.Since = info.Checked
	if info.Title != "" && info.Title == prev.Title && !prev.Since.IsZero() {
		info.Since = prev.Since
	}

	s.mu.Lock()
	e.info, e.done = info, nil
	s.mu.Unlock()
	close(done)
	return info
}

// Read opens a stream once and returns what it says is playing. A stream
// without metadata gives ErrNoMetadata together with the station headers.
func Read(ctx context.Context, client *http.Client, streamURL string) (Info, error) {
	return read(ctx, client, streamURL, 0)
}

func read(ctx context.Context, client *http.Client, streamURL string, depth int) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return Info{}, err
	}
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", "iptv-portal/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("server answered %s", resp.Status)
	}
	info := Info{
		Station:     header(resp, "icy-name"),
		Genre:       header(resp, "icy-genre"),
		Description: header(resp, "icy-description"),
	}
	br := bufio.NewReader(io.LimitReader(resp.Body, maxBody))
	if mi := resp.Header.Get("icy-metaint"); mi != "" {
		n, err := strconv.Atoi(strings.TrimSpace(mi))
		if err != nil || n <= 0 || n > maxMetaInt {
			return info, fmt.Errorf("invalid icy-metaint %q", mi)
		}
		title, err := icyTitle(br, n)
		if err != nil {
			return info, err
		}
		info.setTitle(title)
		return info, nil
	}
	if isPlaylist(resp, br) {
		if depth > 2 {
			return info, errors.New("too many nested playlists")
		}
		next, title, err := hlsTitle(br, resp.Request.URL)
		if err != nil {
			return info, err
		}
		if next != "" {
			sub, err := read(ctx, client, next, depth+1)
			if sub.Station == "" {
				sub.Station, sub.Genre, sub.Description = info.Station, info.Genre, info.Description
			}
			return sub, err
		}
		if title == "" {
			return info, ErrNoMetadata
		}
		info.setTitle(title)
		return info, nil
	}
	return info, ErrNoMetadata
}

func header(resp *http.Response, name string) string {
	return clean(resp.Header.Get(name))
}

func (i *Info) setTitle(t string) {
	i.Title = t
	i.Artist, i.Song = "", t
	if a, s, ok := strings.Cut(t, " - "); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(s) != "" {
		i.Artist, i.Song = strings.TrimSpace(a), strings.TrimSpace(s)
	}
}

// icyTitle skips metaint bytes of audio and reads the metadata block after
// it. A block of length 0 means "unchanged"; the next one is tried then.
func icyTitle(r *bufio.Reader, metaint int) (string, error) {
	for range 3 {
		if _, err := r.Discard(metaint); err != nil {
			return "", fmt.Errorf("reading stream: %w", err)
		}
		n, err := r.ReadByte()
		if err != nil {
			return "", fmt.Errorf("reading stream: %w", err)
		}
		if n == 0 {
			continue
		}
		meta := make([]byte, int(n)*16)
		if _, err := io.ReadFull(r, meta); err != nil {
			return "", fmt.Errorf("reading stream: %w", err)
		}
		t, ok := streamTitle(string(meta))
		if !ok {
			return "", ErrNoMetadata
		}
		return t, nil
	}
	return "", ErrNoMetadata
}

// streamTitle extracts StreamTitle from "StreamTitle='A - B';StreamUrl=”;".
// Titles may contain quotes, so the value ends at "';" or the end.
func streamTitle(meta string) (string, bool) {
	meta = strings.TrimRight(meta, "\x00")
	_, rest, ok := strings.Cut(meta, "StreamTitle='")
	if !ok {
		return "", false
	}
	if i := strings.Index(rest, "';"); i >= 0 {
		rest = rest[:i]
	} else {
		rest = strings.TrimSuffix(rest, "'")
	}
	t := clean(rest)
	// Some stations send only the separator or their own name padding.
	if strings.Trim(t, " -") == "" {
		return "", false
	}
	return t, true
}

// clean makes server text valid UTF-8 (Latin-1 is common) and trims it.
func clean(s string) string {
	if !utf8.ValidString(s) {
		b := make([]rune, 0, len(s))
		for i := 0; i < len(s); i++ {
			b = append(b, rune(s[i]))
		}
		s = string(b)
	}
	return strings.Join(strings.Fields(s), " ")
}

func isPlaylist(resp *http.Response, br *bufio.Reader) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "mpegurl") {
		return true
	}
	head, _ := br.Peek(7)
	return string(head) == "#EXTM3U"
}

// hlsTitle reads an HLS playlist. For a master playlist it returns the
// first variant to read next; for a media playlist the title of the last
// segment, which some stations set to what is playing, either as plain
// text or as title="..." / artist="..." attributes.
func hlsTitle(r io.Reader, base *url.URL) (next, title string, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	variant := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			variant = true
		case strings.HasPrefix(line, "#EXTINF:"):
			_, t, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			title = extinfTitle(t)
		case line != "" && !strings.HasPrefix(line, "#") && variant:
			u, err := base.Parse(line)
			if err != nil {
				return "", "", err
			}
			return u.String(), "", nil
		}
	}
	return "", title, sc.Err()
}

func extinfTitle(t string) string {
	t = strings.TrimSpace(t)
	if !strings.Contains(t, `="`) {
		return clean(t)
	}
	attrs := map[string]string{}
	for t != "" {
		k, rest, ok := strings.Cut(t, `="`)
		if !ok {
			break
		}
		v, rest, _ := strings.Cut(rest, `"`)
		attrs[strings.ToLower(strings.Trim(k, " ,"))] = clean(v)
		t = rest
	}
	switch a, s := attrs["artist"], attrs["title"]; {
	case a != "" && s != "":
		return a + " - " + s
	default:
		return s
	}
}

// icyConn turns an "ICY 200 OK" status line into "HTTP/1.0 200 OK".
type icyConn struct {
	net.Conn
	checked bool
	pending []byte
}

func (c *icyConn) Read(p []byte) (int, error) {
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	if c.checked {
		return c.Conn.Read(p)
	}
	c.checked = true
	buf := make([]byte, 4)
	n, err := io.ReadFull(c.Conn, buf)
	buf = buf[:n]
	if n == 4 && string(buf) == "ICY " {
		buf = []byte("HTTP/1.0 ")
	}
	c.pending = buf
	if len(c.pending) == 0 {
		return 0, err
	}
	n = copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
