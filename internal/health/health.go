// Package health checks whether the stream behind each channel still
// plays: it loads the HLS playlist like a player would, follows a master
// playlist to its first variant and fetches a media segment. Results are
// stored per channel, and channels that keep failing can be switched off
// automatically.
package health

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

const (
	// requestTimeout limits every single request of a check.
	requestTimeout = 15 * time.Second
	// maxPlaylist is the most read of a playlist.
	maxPlaylist = 1 << 20
	// workers is how many channels are checked at the same time.
	workers = 8
)

// Status describes the checker's current and last run.
type Status struct {
	Running  bool
	Done     int // channels checked in the current run
	Total    int // channels in the current run
	LastRun  time.Time
	Duration time.Duration // of the last finished run
}

// Checker checks channel streams.
type Checker struct {
	store   *store.Store
	log     *slog.Logger
	client  *http.Client
	trigger chan struct{}

	mu       sync.Mutex
	status   Status
	afterRun func(context.Context)
}

// New creates a checker. Call Loop to run it periodically.
func New(st *store.Store, log *slog.Logger) *Checker {
	return &Checker{store: st, log: log, trigger: make(chan struct{}, 1),
		client: &http.Client{Timeout: requestTimeout}}
}

// Status returns the progress of the current run and when the last one ran.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// AfterRun sets a function called after every run of CheckAll, e.g. to
// look for replacements of the streams that failed.
func (c *Checker) AfterRun(fn func(context.Context)) {
	c.mu.Lock()
	c.afterRun = fn
	c.mu.Unlock()
}

// Start asks the loop to check all channels now. It returns false when a
// run is already going on.
func (c *Checker) Start() bool {
	if c.Status().Running {
		return false
	}
	select {
	case c.trigger <- struct{}{}:
	default:
	}
	return true
}

// Loop checks all channels whenever the configured interval has passed
// since the last run, and when Start is called, until ctx is done. Interval
// changes apply without a restart.
func (c *Checker) Loop(ctx context.Context) {
	if last, err := c.lastChecked(ctx); err == nil {
		c.mu.Lock()
		c.status.LastRun = last
		c.mu.Unlock()
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.trigger:
			c.CheckAll(ctx)
		case <-t.C:
			hs, err := c.store.HealthSettings(ctx)
			if err != nil {
				c.log.Warn("reading stream check settings", "err", err)
				continue
			}
			if hs.Interval() > 0 && time.Since(c.Status().LastRun) >= hs.Interval() {
				c.CheckAll(ctx)
			}
		}
	}
}

// lastChecked is when a channel was last checked, so a restart does not
// start a run right away.
func (c *Checker) lastChecked(ctx context.Context) (time.Time, error) {
	all, err := c.store.ChannelHealth(ctx)
	var last time.Time
	for _, h := range all {
		if h.CheckedAt.After(last) {
			last = h.CheckedAt
		}
	}
	return last, err
}

// CheckAll checks every channel, enabled or not.
func (c *Checker) CheckAll(ctx context.Context) {
	chs, err := c.store.Channels(ctx, store.ChannelFilter{})
	if err != nil {
		c.log.Warn("listing channels to check", "err", err)
		return
	}
	hs, err := c.store.HealthSettings(ctx)
	if err != nil {
		c.log.Warn("reading stream check settings", "err", err)
		return
	}
	start := time.Now()
	c.mu.Lock()
	if c.status.Running {
		c.mu.Unlock()
		return
	}
	c.status.Running, c.status.Done, c.status.Total = true, 0, len(chs)
	c.mu.Unlock()

	jobs := make(chan store.Channel)
	var wg sync.WaitGroup
	var failed int
	for range min(workers, len(chs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ch := range jobs {
				h, err := c.check(ctx, ch, hs.DisableAfter)
				c.mu.Lock()
				c.status.Done++
				if err == nil && !h.OK {
					failed++
				}
				c.mu.Unlock()
			}
		}()
	}
	for _, ch := range chs {
		if ctx.Err() != nil {
			break
		}
		jobs <- ch
	}
	close(jobs)
	wg.Wait()

	c.mu.Lock()
	c.status.Running = false
	c.status.LastRun = start
	c.status.Duration = time.Since(start)
	after := c.afterRun
	c.mu.Unlock()
	c.log.Info("stream check finished", "channels", len(chs), "failing", failed,
		"took", time.Since(start).Round(time.Second))
	if after != nil && ctx.Err() == nil {
		after(ctx)
	}
}

// CheckChannel checks a single channel now.
func (c *Checker) CheckChannel(ctx context.Context, ch store.Channel) (store.ChannelHealth, error) {
	hs, err := c.store.HealthSettings(ctx)
	if err != nil {
		return store.ChannelHealth{}, err
	}
	return c.check(ctx, ch, hs.DisableAfter)
}

func (c *Checker) check(ctx context.Context, ch store.Channel, disableAfter int) (store.ChannelHealth, error) {
	msg, err := c.Probe(ctx, ch.StreamURL)
	if ctx.Err() != nil {
		return store.ChannelHealth{}, ctx.Err() // shutting down; not the stream's fault
	}
	ok := err == nil
	if !ok {
		msg = err.Error()
	}
	h, change, err := c.store.RecordHealth(ctx, ch.ID, ok, msg, disableAfter)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("storing stream check", "channel", ch.Name, "err", err)
		}
		return h, err
	}
	switch change {
	case store.HealthDisabled:
		c.log.Warn("channel switched off after failed stream checks", "channel", ch.Name,
			"failures", h.Failures, "reason", msg)
	case store.HealthReenabled:
		c.log.Info("channel switched back on, its stream works again", "channel", ch.Name)
	}
	return h, nil
}

// Probe checks one stream URL. It returns a short description of what was
// found, or an error saying why the stream does not play.
func (c *Checker) Probe(ctx context.Context, streamURL string) (string, error) {
	body, final, ctype, err := c.get(ctx, streamURL, maxPlaylist)
	if err != nil {
		return "", err
	}
	if !isPlaylist(body) {
		if looksLikeHLS(streamURL, ctype) {
			return "", errors.New("the response is not an HLS playlist")
		}
		if len(body) == 0 {
			return "", errors.New("the stream sent no data")
		}
		return "stream responds", nil
	}
	variants := 0
	if isMaster(body) {
		uris := playlistURIs(body, final)
		variants = len(uris)
		if variants == 0 {
			return "", errors.New("the master playlist lists no streams")
		}
		body, final, _, err = c.get(ctx, uris[0], maxPlaylist)
		if err != nil {
			return "", fmt.Errorf("first variant: %w", err)
		}
		if !isPlaylist(body) || isMaster(body) {
			return "", errors.New("the first variant is not a media playlist")
		}
	}
	segs := playlistURIs(body, final)
	if len(segs) == 0 {
		return "", errors.New("the playlist has no segments")
	}
	// The newest segment is the one least likely to have expired already.
	if _, _, _, err := c.get(ctx, segs[len(segs)-1], 64<<10); err != nil {
		return "", fmt.Errorf("segment: %w", err)
	}
	if variants > 0 {
		return fmt.Sprintf("HLS, %d %s, segments load", variants, plural(variants, "variant", "variants")), nil
	}
	return "HLS, segments load", nil
}

// get fetches u and reads at most limit bytes of the body. It returns the
// body, the URL after redirects and the content type.
func (c *Checker) get(ctx context.Context, u string, limit int64) ([]byte, *url.URL, string, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", "iptv-portal/"+version.Version)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, "", describe(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, "", fmt.Errorf("HTTP %s", resp.Status)
	}
	// A live MPEG-TS or radio stream never ends, so only the start is read
	// and a timeout after some data has arrived is fine.
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	if (strings.HasPrefix(ctype, "audio/") && !strings.Contains(ctype, "mpegurl")) || strings.HasPrefix(ctype, "video/mp2t") {
		limit = min(limit, 64<<10)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil && len(body) == 0 {
		return nil, nil, "", describe(err)
	}
	return body, resp.Request.URL, ctype, nil
}

// describe shortens network errors to what matters to an admin.
func describe(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
		return errors.New("no answer within " + requestTimeout.String())
	}
	// "dial tcp 192.0.2.1:443: connect: connection refused" becomes
	// "connection refused".
	var oe *net.OpError
	if errors.As(err, &oe) {
		err = oe.Err
		var se *os.SyscallError
		if errors.As(err, &se) {
			err = se.Err
		}
	}
	return err
}

func trimBOM(b []byte) []byte {
	return bytes.TrimLeft(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), " \t\r\n")
}

func isPlaylist(b []byte) bool { return bytes.HasPrefix(trimBOM(b), []byte("#EXTM3U")) }

func isMaster(b []byte) bool { return bytes.Contains(b, []byte("#EXT-X-STREAM-INF")) }

func looksLikeHLS(u, ctype string) bool {
	ctype = strings.ToLower(ctype)
	if strings.Contains(ctype, "mpegurl") {
		return true
	}
	p, err := url.Parse(u)
	return err == nil && strings.HasSuffix(strings.ToLower(p.Path), ".m3u8")
}

// playlistURIs returns the URIs in a playlist (variants of a master
// playlist, segments of a media playlist), resolved against base.
func playlistURIs(b []byte, base *url.URL) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxPlaylist)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ref, err := url.Parse(line)
		if err != nil {
			continue
		}
		out = append(out, base.ResolveReference(ref).String())
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
