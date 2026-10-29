// Package activity keeps track of which users are using their player right
// now, for the admin dashboard.
//
// The portal never carries the video itself: /live/... redirects the player
// to the broadcaster's stream. So all the portal sees is the player's
// requests (login, channel list, guide) and the moment a channel is opened.
// "Online" therefore means "made a request recently", and the channel shown
// is the last one the player opened; the portal cannot tell when someone
// stops watching. Activity is kept in memory and starts empty after a
// restart.
//
// The same activity limits how many places a user can watch from at once
// (see Open): a connection holds a slot while it is watching or, after
// opening a stream, keeps making requests.
package activity

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// OnlineWindow is how recent the last request must be for a connection to
// count as online.
const OnlineWindow = 15 * time.Minute

// keep is how long a connection is remembered after its last request.
const keep = 24 * time.Hour

// Connection is one player (a user on one IP address).
type Connection struct {
	UserID    int64
	Username  string
	IP        string
	UserAgent string
	LastSeen  time.Time
	// Channel is the name of the last channel opened, ChannelAt when.
	Channel   string
	ChannelID int64
	ChannelAt time.Time
}

// Online reports whether the connection made a request within OnlineWindow
// of now.
func (c Connection) Online(now time.Time) bool { return now.Sub(c.LastSeen) < OnlineWindow }

// Watching reports whether the last channel was opened within OnlineWindow
// of now, which is the best hint the portal has that it is still playing.
func (c Connection) Watching(now time.Time) bool {
	return c.Channel != "" && now.Sub(c.ChannelAt) < OnlineWindow
}

type key struct {
	user int64
	ip   string
}

// Tracker records player activity. The zero value is not usable; use New.
type Tracker struct {
	mu    sync.Mutex
	conns map[key]*Connection
	now   func() time.Time
	// Proxy counts the On Demand streams the portal passes through.
	Proxy Proxy
}

// New returns an empty tracker.
func New() *Tracker {
	return &Tracker{conns: map[key]*Connection{}, now: time.Now}
}

func (t *Tracker) touch(userID int64, username, ip, userAgent string) *Connection {
	k := key{userID, ip}
	c := t.conns[k]
	if c == nil {
		c = &Connection{UserID: userID, IP: ip}
		t.conns[k] = c
	}
	c.Username = username
	if userAgent != "" {
		c.UserAgent = userAgent
	}
	c.LastSeen = t.now()
	return c
}

// Active reports whether the connection holds one of the user's
// connection slots: it opened a stream recently, or opened one before and
// is still making requests (players fetch the guide while playing).
func (c Connection) Active(now time.Time) bool {
	return c.Watching(now) || (c.Channel != "" && c.Online(now))
}

// Seen records an authenticated request from a player.
func (t *Tracker) Seen(userID int64, username, ip, userAgent string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.touch(userID, username, ip, userAgent)
}

// Play records that a player opened a channel.
func (t *Tracker) Play(userID int64, username, ip, userAgent string, channelID int64, channel string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.touch(userID, username, ip, userAgent)
	c.Channel, c.ChannelID, c.ChannelAt = channel, channelID, c.LastSeen
}

// Open records that a player opens a stream, unless the user already
// watches from limit other places (IP addresses); then it only records the
// request and returns false. A limit of 0 means no limit.
func (t *Tracker) Open(userID int64, username, ip, userAgent string, channelID int64, channel string, limit int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if limit > 0 && t.active(userID, ip) >= limit {
		t.touch(userID, username, ip, userAgent)
		return false
	}
	c := t.touch(userID, username, ip, userAgent)
	c.Channel, c.ChannelID, c.ChannelAt = channel, channelID, c.LastSeen
	return true
}

// active counts the user's active connections other than the one from
// exceptIP. The caller holds t.mu.
func (t *Tracker) active(userID int64, exceptIP string) int {
	now := t.now()
	n := 0
	for k, c := range t.conns {
		if k.user == userID && k.ip != exceptIP && c.Active(now) {
			n++
		}
	}
	return n
}

// ActiveConnections returns how many connection slots the user holds now.
func (t *Tracker) ActiveConnections(userID int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active(userID, "")
}

// Release frees the user's connection slots, for instance after the user
// moved to another place. The connections stay listed as online.
func (t *Tracker) Release(userID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, c := range t.conns {
		if k.user == userID {
			c.Channel, c.ChannelID, c.ChannelAt = "", 0, time.Time{}
		}
	}
}

// Snapshot is the activity at one moment.
type Snapshot struct {
	Now time.Time
	// Connections seen in the last 24 hours, most recent first.
	Connections []Connection
	// OnlineUsers and Watching count distinct users.
	OnlineUsers int
	Watching    int
}

// Snapshot returns the current activity and forgets connections older
// than 24 hours.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	s := Snapshot{Now: now}
	online, watching := map[int64]bool{}, map[int64]bool{}
	for k, c := range t.conns {
		if now.Sub(c.LastSeen) > keep {
			delete(t.conns, k)
			continue
		}
		s.Connections = append(s.Connections, *c)
		if c.Online(now) {
			online[c.UserID] = true
		}
		if c.Watching(now) {
			watching[c.UserID] = true
		}
	}
	sort.Slice(s.Connections, func(i, j int) bool { return s.Connections[i].LastSeen.After(s.Connections[j].LastSeen) })
	s.OnlineUsers, s.Watching = len(online), len(watching)
	return s
}

// Proxy counts On Demand playback that the portal streams itself (media
// servers with "proxy playback" on): those streams do pass through the
// server, unlike live channels. The zero value is ready to use.
type Proxy struct {
	streams atomic.Int64
	bytes   atomic.Uint64
}

// Start records that a proxied stream starts; call the returned function
// when it ends.
func (p *Proxy) Start() (done func()) {
	p.streams.Add(1)
	var once sync.Once
	return func() { once.Do(func() { p.streams.Add(-1) }) }
}

// Add counts n bytes sent to a player.
func (p *Proxy) Add(n int) {
	if n > 0 {
		p.bytes.Add(uint64(n))
	}
}

// Streams returns how many proxied streams are playing now.
func (p *Proxy) Streams() int { return int(p.streams.Load()) }

// Bytes returns how many bytes were proxied since the start.
func (p *Proxy) Bytes() uint64 { return p.bytes.Load() }
