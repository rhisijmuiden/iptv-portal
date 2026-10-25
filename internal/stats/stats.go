// Package stats records what users watch, for the viewing statistics in
// the admin panel.
//
// The portal never carries the stream: /live/... redirects the player to
// the broadcaster. So it knows when a channel is opened, but not when the
// player stops. How long a view lasted is estimated from what the player
// does next on the same connection (user and IP address):
//
//   - Opening a channel starts a view that is assumed to last Assume, the
//     time the dashboard shows the user as watching.
//   - Opening another channel within MaxGap ends the view at that moment,
//     shorter or longer than assumed: that is someone zapping.
//   - Opening the same channel again, or any other request (players fetch
//     the guide while playing), within Assume of the end keeps the view
//     going.
//
// Views are counted exactly; viewing time is an estimate.
package stats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/activity"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Assume is how long a view lasts when nothing else is heard from the
// player.
const Assume = activity.OnlineWindow

// MaxGap is how long after its start a view can still be ended by opening
// another channel. A player that opens a channel the next morning was not
// watching the previous one all night.
const MaxGap = 4 * time.Hour

// minWrite keeps a chatty player from writing to the database on every
// request: the end of a view moves in steps of at least this.
const minWrite = time.Minute

type key struct {
	user int64
	ip   string
}

type view struct {
	id        int64
	channelID int64
	channel   string
	start     time.Time
	end       time.Time // as stored
}

// Recorder records views in the store. The zero value is not usable; use
// New. A nil *Recorder records nothing.
type Recorder struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time

	mu    sync.Mutex
	open  map[key]*view
	swept time.Time
}

// New returns a recorder writing to st.
func New(st *store.Store, log *slog.Logger) *Recorder {
	return &Recorder{store: st, log: log, now: time.Now, open: map[key]*view{}}
}

// Open records that a user opened a channel (channelID > 0) or an On
// Demand title (channelID 0, kind store.KindVOD).
func (r *Recorder) Open(ctx context.Context, userID int64, username, ip, userAgent string, channelID int64, name, kind string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.sweep(now)
	k := key{userID, ip}
	if v := r.open[k]; v != nil {
		if v.channelID == channelID && v.channel == name && now.Before(v.end.Add(Assume)) {
			// The same channel again: the player reconnected.
			r.extend(ctx, v, now.Add(Assume), true)
			return
		}
		if now.Sub(v.start) < MaxGap {
			r.extend(ctx, v, now, true)
		}
	}
	v := &view{channelID: channelID, channel: name, start: now, end: now.Add(Assume)}
	id, err := r.store.StartView(ctx, store.View{UserID: userID, Username: username, ChannelID: channelID,
		Channel: name, Kind: kind, IP: ip, UserAgent: userAgent, StartedAt: v.start, EndedAt: v.end})
	if err != nil {
		r.log.Warn("recording view", "err", err)
		delete(r.open, k)
		return
	}
	v.id = id
	r.open[k] = v
}

// Seen records any other request from a player: it keeps the view of that
// connection going when it is still playing.
func (r *Recorder) Seen(ctx context.Context, userID int64, ip string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.open[key{userID, ip}]
	if v == nil {
		return
	}
	now := r.now()
	if now.Before(v.end.Add(Assume)) {
		r.extend(ctx, v, now, false)
	}
}

// extend moves the end of v to end: only later unless exact, and only
// when it moves enough to be worth a write. The caller holds r.mu.
func (r *Recorder) extend(ctx context.Context, v *view, end time.Time, exact bool) {
	d := end.Sub(v.end)
	if d < 0 && !exact || d >= 0 && d < minWrite || d < 0 && -d < time.Second {
		return
	}
	if err := r.store.SetViewEnd(ctx, v.id, end); err != nil {
		r.log.Warn("recording view", "err", err)
		return
	}
	v.end = end
}

// sweep forgets views that can no longer change. The caller holds r.mu.
func (r *Recorder) sweep(now time.Time) {
	if now.Sub(r.swept) < 10*time.Minute {
		return
	}
	r.swept = now
	for k, v := range r.open {
		if now.Sub(v.start) > MaxGap && now.After(v.end.Add(Assume)) {
			delete(r.open, k)
		}
	}
}

// Purge removes views older than the retention set in the store.
func (r *Recorder) Purge(ctx context.Context) error {
	days, err := r.store.ViewRetention(ctx)
	if err != nil || days == 0 {
		return err
	}
	n, err := r.store.PurgeViews(ctx, r.now().AddDate(0, 0, -days))
	if n > 0 {
		r.log.Info("removed old viewing statistics", "views", n, "days", days)
	}
	return err
}

// Forget drops the views being recorded, after the statistics were
// cleared.
func (r *Recorder) Forget() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open = map[key]*view{}
}

// Loop removes old views once a day until ctx is done.
func (r *Recorder) Loop(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if err := r.Purge(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("removing old viewing statistics", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
