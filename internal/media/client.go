// Package media reads the movie and series libraries of Jellyfin and Plex
// servers, so the portal can offer them to players as On Demand.
package media

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

// LibraryType says what a library holds.
type LibraryType string

// Library types the portal can offer; other libraries (music, photos) are
// not listed.
const (
	Movies LibraryType = "movies"
	Shows  LibraryType = "shows"
)

// Library is a movie or series library on a media server.
type Library struct {
	ID   string
	Name string
	Type LibraryType
}

// ItemType is the kind of an item.
type ItemType int

// Item types.
const (
	Movie ItemType = iota
	Show
	Episode
)

// Item is a movie, a series or an episode.
type Item struct {
	// ID is the portal's numeric id, filled in by the catalog.
	ID       int64
	ServerID int64
	// Key is the item's id on the media server.
	Key  string
	Type ItemType
	// CategoryID is the portal id of the item's library.
	CategoryID int64

	Title         string
	Plot          string
	Year          int
	Released      string // YYYY-MM-DD when known
	Genres        []string
	Directors     []string
	Cast          []string
	Rating        float64 // 0-10
	ContentRating string
	Duration      time.Duration
	Added         time.Time
	// Container is the file extension of the video, e.g. "mkv".
	Container string
	TMDBID    string

	// Poster, Backdrop and Stream are paths on the media server, without
	// credentials; see Client.URL.
	Poster   string
	Backdrop string
	Stream   string

	// Episodes only.
	ShowKey string
	ShowID  int64
	Season  int
	Number  int
}

// Client talks to one media server.
type Client interface {
	// Libraries lists the movie and series libraries.
	Libraries(ctx context.Context) ([]Library, error)
	// Items returns the movies of a movie library, or the series and their
	// episodes of a series library.
	Items(ctx context.Context, lib Library) ([]Item, error)
	// URL turns a path from an item into a full address including the
	// token.
	URL(path string) string
}

// NewClient returns the client for a media server.
func NewClient(s store.MediaServer, hc *http.Client) (Client, error) {
	if hc == nil {
		hc = NewHTTPClient(2 * time.Minute)
	}
	base := strings.TrimRight(s.URL, "/")
	switch s.Kind {
	case store.MediaJellyfin:
		return &jellyfin{base: base, token: s.Token, hc: hc}, nil
	case store.MediaPlex:
		return &plex{base: base, token: s.Token, hc: hc}, nil
	}
	return nil, fmt.Errorf("unknown media server kind %q", s.Kind)
}

// pageSize is the number of items asked for per request. Large libraries
// are read in pages, so no single request takes long enough to time out.
var pageSize = 200

// NewHTTPClient returns an HTTP client for media servers. Connecting gets
// its own short timeout, so an unreachable server is reported as such
// instead of as a slow answer; timeout limits each request as a whole.
func NewHTTPClient(timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	return &http.Client{Transport: tr, Timeout: timeout}
}

// requestError turns a failed request into a message that says what to
// check. The URL is left out because it may hold the token.
func requestError(host string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		if op.Timeout() {
			return fmt.Errorf("no connection to %s within 10 seconds. Check the address and port, and that this machine can reach the server (firewall, VLAN, Docker port mapping)", host)
		}
		return fmt.Errorf("cannot connect to %s: %v. Check the address and port", host, op.Err)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return fmt.Errorf("cannot find %s: %v", host, dns.Err)
	}
	var tlsErr *tls.RecordHeaderError
	if errors.As(err, &tlsErr) || strings.Contains(err.Error(), "HTTP response to HTTPS client") {
		return fmt.Errorf("%s does not speak HTTPS; use an address starting with http://", host)
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return fmt.Errorf("connected to %s, but the server did not answer in time. It may be busy (for example scanning a library); try again later", host)
	}
	return fmt.Errorf("cannot reach %s: %w", host, err)
}

// withQuery appends a query parameter to a path that may already have a
// query.
func withQuery(p, key, value string) string {
	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}
	return p + sep + key + "=" + url.QueryEscape(value)
}

// getJSON fetches base+path with the given headers and decodes the JSON
// answer into v.
func getJSON(ctx context.Context, hc *http.Client, rawURL string, header http.Header, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "iptv-portal/"+version.Version)
	resp, err := hc.Do(req)
	if err != nil {
		return requestError(req.URL.Host, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("the server refused the token (" + resp.Status + ")")
	case resp.StatusCode != http.StatusOK:
		return errors.New("the server answered " + resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512<<20)).Decode(v); err != nil {
		return fmt.Errorf("unexpected answer from the server: %w", err)
	}
	return nil
}

// ext returns the file extension of a path without the dot, lower case.
func ext(p string) string {
	if i := strings.LastIndexAny(p, "./\\"); i >= 0 && p[i] == '.' {
		return strings.ToLower(p[i+1:])
	}
	return ""
}
