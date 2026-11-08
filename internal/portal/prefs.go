package portal

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// This file applies what users set on their account pages (favourites,
// their own order of groups, hidden groups and parental controls) to what
// players get.

// favouritesCategory is the Xtream category id of the Favourites group.
const favouritesCategory = 999999998

// playerView returns the user's channels as their player gets them.
func (h *Handler) playerView(ctx context.Context, u store.User) (store.PlayerView, store.Preferences, error) {
	chs, err := h.store.UserChannels(ctx, u.ID)
	if err != nil {
		return store.PlayerView{}, store.Preferences{}, err
	}
	prefs, err := h.store.Preferences(ctx, u.ID)
	if err != nil {
		return store.PlayerView{}, prefs, err
	}
	return prefs.Apply(chs, time.Now()), prefs, nil
}

// vodGate decides which On Demand titles a user's player gets.
type vodGate struct {
	maxAge int
	locked map[int64]bool // locked category ids
}

// vodGate returns the On Demand parental controls of a user; nil means
// everything is allowed.
func (h *Handler) vodGate(ctx context.Context, u store.User) (*vodGate, error) {
	prefs, err := h.store.Preferences(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	p := prefs.Parental
	if p.Unlocked(time.Now()) || (p.MaxAge == 0 && len(p.LockedMedia) == 0) {
		return nil, nil
	}
	g := &vodGate{maxAge: p.MaxAge, locked: map[int64]bool{}}
	for _, t := range []media.LibraryType{media.Movies, media.Shows} {
		for _, c := range h.vod.Categories(t) {
			if p.MediaLocked(string(t), c.Name) {
				g.locked[c.ID] = true
			}
		}
	}
	return g, nil
}

// allows reports whether the player may get the movie or series it.
func (g *vodGate) allows(it *media.Item) bool {
	if g == nil {
		return true
	}
	for _, c := range it.CategoryIDs {
		if g.locked[c] {
			return false
		}
	}
	if g.maxAge > 0 {
		if age, ok := ageRating(it.ContentRating); ok && age > g.maxAge {
			return false
		}
	}
	return true
}

// allowsCategory reports whether the On Demand category is shown.
func (g *vodGate) allowsCategory(id int64) bool { return g == nil || !g.locked[id] }

// knownRatings are age ratings that do not carry their age as a number.
var knownRatings = map[string]int{
	"G": 0, "PG": 8, "PG-13": 13, "R": 17, "NC-17": 18, "X": 18,
	"TV-Y": 0, "TV-Y7": 7, "TV-Y7-FV": 7, "TV-G": 0, "TV-PG": 10, "TV-14": 14, "TV-MA": 17,
	"U": 0, "UC": 0, "12A": 12, "R18": 18,
	"AL": 0, "ALL": 0, "A": 0, "0": 0, "KT": 0, "MG6": 6,
	"APPROVED": 0, "PASSED": 0,
}

var ratingNumber = regexp.MustCompile(`(?:^|\D)(\d{1,2})(?:\D|$)`)

// ageRating reads the minimum age from a content rating such as "PG-13",
// "TV-MA", "NL-12", "FSK 16" or "16+". ok is false for an unknown or
// missing rating; such titles are always shown.
func ageRating(r string) (age int, ok bool) {
	r = strings.ToUpper(strings.TrimSpace(r))
	if r == "" {
		return 0, false
	}
	if a, ok := knownRatings[r]; ok {
		return a, true
	}
	// Strip a country prefix: "NL-12", "de/16", "GB-PG".
	if i := strings.IndexAny(r, "-/ :"); i == 2 {
		if a, ok := knownRatings[r[3:]]; ok {
			return a, true
		}
	}
	if m := ratingNumber.FindStringSubmatch(r); m != nil {
		a, _ := strconv.Atoi(m[1])
		if a <= 21 {
			return a, true
		}
	}
	return 0, false
}
