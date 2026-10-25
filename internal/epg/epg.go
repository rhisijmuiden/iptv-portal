// Package epg downloads XMLTV program guides, merges them into an in-memory
// index and writes per-user guides for the channels a user can watch.
package epg

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Programme is one entry of the guide.
type Programme struct {
	Channel string
	Start   time.Time
	Stop    time.Time
	Title   string
	Desc    string
}

// SourceStatus reports the last download of one guide URL.
type SourceStatus struct {
	URL        string
	Channels   int
	Programmes int
	Error      string
}

// Status describes the guide for the admin panel.
type Status struct {
	UpdatedAt  time.Time
	Sources    []SourceStatus
	Channels   int
	Programmes int
	Refreshing bool
}

// Guide holds the merged program guide.
type Guide struct {
	log     *slog.Logger
	client  *http.Client
	cache   string // file the merged guide is saved to, so it survives restarts
	mu      sync.RWMutex
	progs   map[string][]Programme // channel id -> programmes sorted by start
	names   map[string]string      // channel id -> display name from the source
	status  Status
	refresh sync.Mutex
}

// Keep is how far back and ahead programmes are kept.
const (
	keepPast   = 6 * time.Hour
	keepFuture = 8 * 24 * time.Hour
	maxSize    = 200 << 20 // per source, uncompressed
)

// New creates a guide that caches its data in dataDir and loads an earlier
// cache when present.
func New(log *slog.Logger, dataDir string) *Guide {
	g := &Guide{
		log:    log,
		client: &http.Client{Timeout: 2 * time.Minute},
		cache:  filepath.Join(dataDir, "epg-cache.xml"),
		progs:  map[string][]Programme{},
		names:  map[string]string{},
	}
	if f, err := os.Open(g.cache); err == nil {
		defer f.Close()
		progs, names := map[string][]Programme{}, map[string]string{}
		if _, _, err := parse(f, progs, names); err == nil {
			info, _ := f.Stat()
			g.set(progs, names, Status{UpdatedAt: info.ModTime()})
		} else {
			log.Warn("reading cached guide", "err", err)
		}
	}
	return g
}

// Status returns the state of the guide.
func (g *Guide) Status() Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.status
}

func (g *Guide) set(progs map[string][]Programme, names map[string]string, st Status) {
	n := 0
	for id, ps := range progs {
		sort.Slice(ps, func(i, j int) bool { return ps[i].Start.Before(ps[j].Start) })
		progs[id] = dedupe(ps)
		n += len(progs[id])
	}
	st.Channels, st.Programmes = len(progs), n
	g.mu.Lock()
	g.progs, g.names, g.status = progs, names, st
	g.mu.Unlock()
}

// dedupe drops programmes that start at the same time as the previous one,
// which happens when two sources cover the same channel.
func dedupe(ps []Programme) []Programme {
	out := ps[:0]
	for i, p := range ps {
		if i > 0 && p.Start.Equal(out[len(out)-1].Start) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Refresh downloads all sources and replaces the guide. Sources that fail
// are reported in the status; the others are still used.
func (g *Guide) Refresh(ctx context.Context, urls []string) error {
	if !g.refresh.TryLock() {
		return errors.New("a refresh is already running")
	}
	defer g.refresh.Unlock()
	g.mu.Lock()
	g.status.Refreshing = true
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.status.Refreshing = false
		g.mu.Unlock()
	}()

	progs, names := map[string][]Programme{}, map[string]string{}
	st := Status{UpdatedAt: time.Now()}
	for _, u := range urls {
		s := SourceStatus{URL: u}
		chs, n, err := g.fetch(ctx, u, progs, names)
		s.Channels, s.Programmes = chs, n
		if err != nil {
			s.Error = err.Error()
			g.log.Warn("downloading guide", "url", u, "err", err)
		}
		st.Sources = append(st.Sources, s)
	}
	failed := 0
	for _, s := range st.Sources {
		if s.Error != "" {
			failed++
		}
	}
	if failed > 0 && failed == len(urls) {
		// Keep the previous guide rather than leaving players with nothing,
		// e.g. when the network is briefly down.
		g.mu.Lock()
		st.UpdatedAt = g.status.UpdatedAt
		st.Channels, st.Programmes = g.status.Channels, g.status.Programmes
		g.status = st
		g.mu.Unlock()
		return errors.New("all guide sources failed")
	}
	g.set(progs, names, st)
	if err := g.save(); err != nil {
		g.log.Warn("saving guide cache", "err", err)
	}
	g.log.Info("guide refreshed", "channels", st.Channels, "programmes", st.Programmes)
	return nil
}

func (g *Guide) fetch(ctx context.Context, url string, progs map[string][]Programme, names map[string]string) (int, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "iptv-portal")
	resp, err := g.client.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("server answered %s", resp.Status)
	}
	r, err := maybeGunzip(resp.Body)
	if err != nil {
		return 0, 0, err
	}
	return parse(io.LimitReader(r, maxSize), progs, names)
}

// maybeGunzip detects gzip by its magic bytes, so both .xml and .xml.gz
// sources work regardless of their Content-Type.
func maybeGunzip(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(2)
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return gzip.NewReader(br)
	}
	return br, nil
}

type xmlChannel struct {
	ID    string   `xml:"id,attr"`
	Names []string `xml:"display-name"`
}

type xmlText struct {
	Lang string `xml:"lang,attr,omitempty"`
	Text string `xml:",chardata"`
}

type xmlProgramme struct {
	Start   string    `xml:"start,attr"`
	Stop    string    `xml:"stop,attr"`
	Channel string    `xml:"channel,attr"`
	Titles  []xmlText `xml:"title"`
	Descs   []xmlText `xml:"desc"`
}

// parse reads an XMLTV document into progs and names, keeping only
// programmes within the retention window. It returns the number of
// channels and programmes read.
func parse(r io.Reader, progs map[string][]Programme, names map[string]string) (int, int, error) {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		// UTF-8 is by far the most common; Latin-1 is handled byte-wise.
		switch strings.ToLower(charset) {
		case "iso-8859-1", "latin1", "latin-1", "windows-1252":
			return &latin1Reader{r: input}, nil
		}
		return input, nil
	}
	from, until := time.Now().Add(-keepPast), time.Now().Add(keepFuture)
	chans, n := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return chans, n, fmt.Errorf("invalid XMLTV: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var c xmlChannel
			if err := dec.DecodeElement(&c, &se); err != nil {
				return chans, n, fmt.Errorf("invalid XMLTV: %w", err)
			}
			if c.ID != "" {
				chans++
				if _, seen := names[c.ID]; !seen && len(c.Names) > 0 {
					names[c.ID] = strings.TrimSpace(c.Names[0])
				}
			}
		case "programme":
			var p xmlProgramme
			if err := dec.DecodeElement(&p, &se); err != nil {
				return chans, n, fmt.Errorf("invalid XMLTV: %w", err)
			}
			start, err1 := ParseTime(p.Start)
			stop, err2 := ParseTime(p.Stop)
			if err1 != nil || p.Channel == "" {
				continue
			}
			if err2 != nil || !stop.After(start) {
				stop = start.Add(time.Hour)
			}
			if stop.Before(from) || start.After(until) {
				continue
			}
			progs[p.Channel] = append(progs[p.Channel], Programme{
				Channel: p.Channel, Start: start, Stop: stop,
				Title: pick(p.Titles), Desc: pick(p.Descs),
			})
			n++
		}
	}
	return chans, n, nil
}

func pick(ts []xmlText) string {
	if len(ts) == 0 {
		return ""
	}
	return strings.TrimSpace(ts[0].Text)
}

// latin1Reader converts ISO-8859-1 bytes to UTF-8.
type latin1Reader struct {
	r       io.Reader
	pending []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(l.pending) == 0 {
		buf := make([]byte, 4096)
		n, err := l.r.Read(buf)
		for _, b := range buf[:n] {
			l.pending = utf8.AppendRune(l.pending, rune(b))
		}
		if n == 0 {
			return 0, err
		}
	}
	n := copy(p, l.pending)
	l.pending = l.pending[n:]
	return n, nil
}

// xmltvLayout is the XMLTV time format, e.g. "20261017120000 +0200".
const xmltvLayout = "20060102150405 -0700"

// ParseTime parses an XMLTV timestamp; the offset is optional (UTC).
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) > 14 && s[14] != ' ' {
		// "20261017120000+0200" without a space
		s = s[:14] + " " + s[14:]
	}
	if len(s) == 14 {
		return time.ParseInLocation("20060102150405", s, time.UTC)
	}
	return time.Parse(xmltvLayout, s)
}

// Programmes returns the programmes of a channel between from and until.
func (g *Guide) Programmes(channel string, from, until time.Time) []Programme {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Programme
	for _, p := range g.progs[channel] {
		if p.Stop.After(from) && p.Start.Before(until) {
			out = append(out, p)
		}
	}
	return out
}

// Has reports whether the guide has programmes for a channel.
func (g *Guide) Has(channel string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.progs[channel]) > 0
}

// Channel is a channel to include in a written guide.
type Channel struct {
	ID   string // XMLTV channel id (the channel's EPG ID)
	Name string
	Logo string
	// Programmes are written when the guide has nothing for ID, e.g. the
	// generated schedule of a radio station.
	Programmes []Programme
}

// WriteXMLTV writes a guide containing only the given channels.
func (g *Guide) WriteXMLTV(w io.Writer, channels []Channel) error {
	bw := bufio.NewWriter(w)
	bw.WriteString(xml.Header)
	bw.WriteString(`<tv generator-info-name="IPTV Portal">` + "\n")
	seen := map[string]bool{}
	var ids []string
	fallback := map[string][]Programme{}
	for _, c := range channels {
		if c.ID == "" || seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		ids = append(ids, c.ID)
		fallback[c.ID] = c.Programmes
		bw.WriteString(`<channel id="` + esc(c.ID) + `"><display-name>` + esc(c.Name) + `</display-name>`)
		if c.Logo != "" {
			bw.WriteString(`<icon src="` + esc(c.Logo) + `"/>`)
		}
		bw.WriteString("</channel>\n")
	}
	g.mu.RLock()
	for _, id := range ids {
		ps := g.progs[id]
		if len(ps) == 0 {
			ps = fallback[id]
		}
		for _, p := range ps {
			writeProgramme(bw, p)
		}
	}
	g.mu.RUnlock()
	bw.WriteString("</tv>\n")
	return bw.Flush()
}

func writeProgramme(bw *bufio.Writer, p Programme) {
	bw.WriteString(`<programme start="` + p.Start.Format(xmltvLayout) + `" stop="` + p.Stop.Format(xmltvLayout) +
		`" channel="` + esc(p.Channel) + `"><title>` + esc(p.Title) + `</title>`)
	if p.Desc != "" {
		bw.WriteString(`<desc>` + esc(p.Desc) + `</desc>`)
	}
	bw.WriteString("</programme>\n")
}

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// save writes the whole guide to the cache file.
func (g *Guide) save() error {
	tmp := g.cache + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	g.mu.RLock()
	var chans []Channel
	for id := range g.progs {
		chans = append(chans, Channel{ID: id, Name: g.names[id]})
	}
	g.mu.RUnlock()
	sort.Slice(chans, func(i, j int) bool { return chans[i].ID < chans[j].ID })
	if err := g.WriteXMLTV(f, chans); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, g.cache)
}

// Loop refreshes the guide on start (when it is older than every) and then
// every interval, until ctx is done. urls is called each time so changed
// settings are picked up. A value on trigger refreshes immediately.
func (g *Guide) Loop(ctx context.Context, every time.Duration, urls func() []string, trigger <-chan struct{}) {
	wait := time.Duration(0)
	if st := g.Status(); !st.UpdatedAt.IsZero() {
		wait = max(0, every-time.Since(st.UpdatedAt))
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-trigger:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		g.Refresh(ctx, urls())
		t.Reset(every)
	}
}
