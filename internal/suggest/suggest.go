// Package suggest looks for a working replacement when the stream of a
// channel keeps failing: TV channels in the iptv-org database, radio
// stations in Radio Browser. Every stream found is tested like the stream
// check does, and only the ones that play are stored as suggestions. A
// channel only changes when an admin accepts one.
package suggest

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rhisijmuiden/iptv-portal/internal/iptvorg"
	"github.com/rhisijmuiden/iptv-portal/internal/radiobrowser"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

const (
	// AutoAfter is how many checks in a row a channel must fail before it
	// is searched automatically, so a single hiccup is left alone.
	AutoAfter = 2
	// retryAfter is how long the automatic search waits before searching
	// a channel that is still failing again.
	retryAfter = 24 * time.Hour
	// maxTests is how many streams are tested per channel.
	maxTests = 8
	// maxSuggestions is how many working streams are kept per channel.
	maxSuggestions = 3
	// testers is how many streams of a channel are tested at the same time.
	testers = 4
)

// Prober tests a stream URL, like health.Checker.Probe: it returns what
// was found, or why the stream does not play.
type Prober func(ctx context.Context, streamURL string) (string, error)

// Status describes the current and the last search over all failing
// channels.
type Status struct {
	Running bool
	Done    int // channels searched in the current run
	Total   int // channels in the current run
	LastRun time.Time
	Found   int // new suggestions of the last finished run
}

// Finder finds replacement streams.
type Finder struct {
	store *store.Store
	iptv  *iptvorg.Client
	radio *radiobrowser.Client
	probe Prober
	log   *slog.Logger

	mu     sync.Mutex
	status Status
}

// New creates a finder.
func New(st *store.Store, iptv *iptvorg.Client, radio *radiobrowser.Client, probe Prober, log *slog.Logger) *Finder {
	return &Finder{store: st, iptv: iptv, radio: radio, probe: probe, log: log}
}

// Status returns the progress of the current run and when the last one
// finished.
func (f *Finder) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

// Start searches every failing channel in the background, also the ones
// searched recently. It returns false when a search is already running.
func (f *Finder) Start() bool {
	f.mu.Lock()
	running := f.status.Running
	f.mu.Unlock()
	if running {
		return false
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		f.run(ctx, true)
	}()
	return true
}

// Auto searches the channels that failed AutoAfter checks in a row and
// were not searched in the last day, when suggestions are switched on. The
// stream checker calls it after every run.
func (f *Finder) Auto(ctx context.Context) {
	hs, err := f.store.HealthSettings(ctx)
	if err != nil {
		f.log.Warn("reading stream check settings", "err", err)
		return
	}
	if hs.Suggest {
		f.run(ctx, false)
	}
}

func (f *Finder) run(ctx context.Context, all bool) {
	chs, err := f.store.Channels(ctx, store.ChannelFilter{})
	if err != nil {
		f.log.Warn("listing channels to find replacements for", "err", err)
		return
	}
	health, err := f.store.ChannelHealth(ctx)
	if err != nil {
		f.log.Warn("reading stream checks", "err", err)
		return
	}
	searched, err := f.store.SearchedAt(ctx)
	if err != nil {
		f.log.Warn("reading stream checks", "err", err)
		return
	}
	var todo []store.Channel
	for _, c := range chs {
		h, ok := health[c.ID]
		if !ok || h.OK {
			continue
		}
		if !all && (h.Failures < AutoAfter || time.Since(searched[c.ID]) < retryAfter) {
			continue
		}
		todo = append(todo, c)
	}
	f.mu.Lock()
	if f.status.Running {
		f.mu.Unlock()
		return
	}
	f.status.Running, f.status.Done, f.status.Total = true, 0, len(todo)
	f.mu.Unlock()

	found := 0
	for _, c := range todo {
		if ctx.Err() != nil {
			break
		}
		n, err := f.Channel(ctx, c)
		if err != nil && ctx.Err() == nil {
			f.log.Warn("looking for a replacement stream", "channel", c.Name, "err", err)
		}
		found += n
		f.mu.Lock()
		f.status.Done++
		f.mu.Unlock()
	}

	f.mu.Lock()
	f.status.Running = false
	f.status.LastRun = time.Now()
	f.status.Found = found
	f.mu.Unlock()
	if len(todo) > 0 {
		f.log.Info("looked for replacement streams", "channels", len(todo), "new suggestions", found)
	}
}

// Channel looks for replacements of one channel now. It returns how many
// new suggestions were stored.
func (f *Finder) Channel(ctx context.Context, c store.Channel) (int, error) {
	var cands []store.Suggestion
	if c.IsRadio() {
		var err error
		if cands, err = f.radioCandidates(ctx, c); err != nil {
			return 0, err
		}
	} else {
		cat, err := f.iptv.Catalog(ctx, false)
		if err != nil {
			return 0, err
		}
		cands = tvCandidates(cat, c)
	}
	used, err := f.store.StreamURLs(ctx)
	if err != nil {
		return 0, err
	}
	dismissed, err := f.store.DismissedStreams(ctx, c.ID)
	if err != nil {
		return 0, err
	}
	var test []store.Suggestion
	for _, s := range cands {
		if s.StreamURL == c.StreamURL || used[s.StreamURL] || dismissed[s.StreamURL] {
			continue
		}
		used[s.StreamURL] = true // drop duplicates
		test = append(test, s)
		if len(test) == maxTests {
			break
		}
	}
	working := f.test(ctx, test)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if len(working) > maxSuggestions {
		working = working[:maxSuggestions]
	}
	return f.store.SaveSuggestions(ctx, c.ID, working)
}

// test probes the streams and returns the ones that play, in their
// original order, with what the test found.
func (f *Finder) test(ctx context.Context, list []store.Suggestion) []store.Suggestion {
	ok := make([]bool, len(list))
	sem := make(chan struct{}, testers)
	var wg sync.WaitGroup
	for i := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			msg, err := f.probe(ctx, list[i].StreamURL)
			if err == nil {
				list[i].Detail, ok[i] = msg, true
			}
		}()
	}
	wg.Wait()
	var out []store.Suggestion
	for i, s := range list {
		if ok[i] {
			out = append(out, s)
		}
	}
	return out
}

// tvCandidates returns the iptv-org streams that may replace the stream of
// c, best first: the channel with c's EPG ID, the channel that lists c's
// current stream, then channels with the same name (in the same country
// when it is known).
func tvCandidates(cat *iptvorg.Catalog, c store.Channel) []store.Suggestion {
	epgID, feed := splitEPGID(c.EPGID)
	country := countryOf(epgID)
	name := normName(c.Name)

	type hit struct {
		ch    iptvorg.Channel
		match string
	}
	var hits [3][]hit // by strength: EPG ID, stream URL, name
	for _, ch := range cat.Channels {
		switch {
		case ch.ID != "" && epgID != "" && strings.EqualFold(ch.ID, epgID):
			hits[0] = append(hits[0], hit{ch, store.MatchEPG})
		case listsStream(ch, c.StreamURL):
			hits[1] = append(hits[1], hit{ch, store.MatchURL})
			if country == "" {
				country = strings.ToLower(ch.Country)
			}
		}
	}
	if name != "" {
		for _, ch := range cat.Channels {
			if normName(ch.Name) != name || (ch.ID != "" && strings.EqualFold(ch.ID, epgID)) || listsStream(ch, c.StreamURL) {
				continue
			}
			if country != "" && !strings.EqualFold(ch.Country, country) {
				continue
			}
			hits[2] = append(hits[2], hit{ch, store.MatchName})
		}
	}

	var out []store.Suggestion
	for _, group := range hits {
		for _, h := range group {
			// The feed the channel used before comes first.
			streams := make([]iptvorg.Stream, 0, len(h.ch.Streams))
			for _, s := range h.ch.Streams {
				if feed != "" && strings.EqualFold(s.Feed, feed) {
					streams = append(streams, s)
				}
			}
			for _, s := range h.ch.Streams {
				if feed == "" || !strings.EqualFold(s.Feed, feed) {
					streams = append(streams, s)
				}
			}
			for _, s := range streams {
				// Players connect straight to the stream and cannot send
				// the headers some streams need.
				if s.NeedsHeaders {
					continue
				}
				out = append(out, store.Suggestion{StreamURL: s.URL, Name: streamName(h.ch, s),
					Source: store.SourceIPTVOrg, SourceID: h.ch.ID, LogoURL: h.ch.Logo, EPGID: h.ch.EPGID(s),
					Match: h.match})
			}
		}
	}
	return out
}

func listsStream(ch iptvorg.Channel, u string) bool {
	for _, s := range ch.Streams {
		if s.URL == u {
			return true
		}
	}
	return false
}

func streamName(ch iptvorg.Channel, s iptvorg.Stream) string {
	name := ch.Name
	if name == "" {
		name = s.Title
	}
	var extra []string
	for _, v := range append([]string{s.Feed, s.Quality}, s.Labels...) {
		if v = strings.TrimSpace(v); v != "" {
			extra = append(extra, v)
		}
	}
	if len(extra) > 0 {
		name += " (" + strings.Join(extra, ", ") + ")"
	}
	return name
}

// radioCandidates returns the Radio Browser stations that may replace the
// stream of c: the station it was imported from, when Radio Browser knows
// a newer address, then stations with the same name.
func (f *Finder) radioCandidates(ctx context.Context, c store.Channel) ([]store.Suggestion, error) {
	var out []store.Suggestion
	add := func(st radiobrowser.Station, match string) {
		if st.URL == "" {
			return
		}
		out = append(out, store.Suggestion{StreamURL: st.URL, Name: stationName(st), Source: store.SourceRadioBrowser,
			SourceID: st.UUID, LogoURL: st.Favicon, Match: match})
	}
	var errs []error
	if c.SourceID != "" {
		sts, err := f.radio.ByUUID(ctx, []string{c.SourceID})
		errs = append(errs, err)
		for _, st := range sts {
			add(st, store.MatchSource)
		}
	}
	if name := normName(c.Name); name != "" {
		sts, err := f.radio.Search(ctx, radiobrowser.Filter{Name: c.Name, Order: radiobrowser.OrderVotes, Limit: 50})
		errs = append(errs, err)
		for _, st := range sts {
			if st.UUID != c.SourceID && normName(st.Name) == name {
				add(st, store.MatchName)
			}
		}
	}
	if len(out) == 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func stationName(st radiobrowser.Station) string {
	var extra []string
	if st.CountryCode != "" {
		extra = append(extra, st.CountryCode)
	}
	if st.Codec != "" {
		if st.Bitrate > 0 {
			extra = append(extra, st.Codec+" "+strconv.Itoa(st.Bitrate)+" kbit/s")
		} else {
			extra = append(extra, st.Codec)
		}
	}
	if len(extra) == 0 {
		return st.Name
	}
	if strings.HasSuffix(st.Name, ")") {
		return st.Name + " · " + strings.Join(extra, ", ")
	}
	return st.Name + " (" + strings.Join(extra, ", ") + ")"
}

// splitEPGID splits "NPO1.nl@HD" into the iptv-org channel id "NPO1.nl"
// and the feed "HD".
func splitEPGID(id string) (string, string) {
	id = strings.TrimSpace(id)
	ch, feed, _ := strings.Cut(id, "@")
	return ch, feed
}

// countryOf returns the country code at the end of an iptv-org channel id:
// "nl" for "NPO1.nl"; "" when there is none.
func countryOf(id string) string {
	i := strings.LastIndex(id, ".")
	if i < 0 || len(id)-i-1 != 2 {
		return ""
	}
	return strings.ToLower(id[i+1:])
}

// noise are words that tell versions of a channel apart rather than name
// it.
var noise = map[string]bool{"hd": true, "sd": true, "fhd": true, "uhd": true, "4k": true, "hq": true,
	"240p": true, "360p": true, "480p": true, "576p": true, "720p": true, "1080i": true, "1080p": true, "2160p": true,
	"aac": true, "mp3": true, "stream": true, "live": true}

// normName reduces a channel name to its letters and digits without the
// parts that tell versions apart, so "NPO 1 HD" and "NPO1 (720p)" match.
func normName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(' || r == '[':
			depth++
			b.WriteRune(' ')
		case (r == ')' || r == ']') && depth > 0:
			depth--
			b.WriteRune(' ')
		case depth > 0:
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	var out strings.Builder
	for _, w := range strings.Fields(b.String()) {
		if !noise[w] {
			out.WriteString(w)
		}
	}
	return out.String()
}
