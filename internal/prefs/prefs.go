// Package prefs holds what both the account pages and the admin panel
// need of the users' own preferences: favourites, channel groups and
// parental controls.
package prefs

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/media"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Groups returns the user's channels and their groups (hidden and
// locked ones included) in the user's order: TV first, then radio.
func Groups(chs []store.Channel, prefs store.Preferences) []store.Category {
	all := prefs
	all.Categories = map[int64]store.UserCategory{}
	for id, c := range prefs.Categories {
		all.Categories[id] = store.UserCategory{Position: c.Position}
	}
	all.Parental = store.Parental{}
	return store.ChannelGroups(all.Apply(chs, time.Now()).Channels)
}

// LockRow is a channel group or On Demand category that can be locked.
type LockRow struct {
	Key    string // form value
	Name   string
	Count  int
	Locked bool
}

// Form is the favourites and lock settings of a user, for the parental
// controls page and the admin's user page.
type Form struct {
	TV, Radio      []LockRow
	Movies, Shows  []LockRow
	OnDemand       bool
	MaxAge         int
	Ages           []int
	Unlocked       bool
	UnlockedUntil  *time.Time
	PINSet         bool
	FavouriteNames []string
	Favourites     bool // the favourites group is on
}

// FormData collects the lock settings of a user for a form.
func FormData(ctx context.Context, st *store.Store, vod *media.Catalog, u store.User) (Form, error) {
	chs, err := st.UserChannels(ctx, u.ID)
	if err != nil {
		return Form{}, err
	}
	prefs, err := st.Preferences(ctx, u.ID)
	if err != nil {
		return Form{}, err
	}
	f := Form{MaxAge: prefs.Parental.MaxAge, Ages: store.AgeLimits, PINSet: prefs.Parental.PINSet,
		Unlocked: prefs.Parental.Unlocked(time.Now()), UnlockedUntil: prefs.Parental.UnlockedUntil,
		Favourites: prefs.FavouritesGroup}
	byID := map[int64]string{}
	for _, c := range chs {
		byID[c.ID] = c.Name
	}
	for _, id := range prefs.Favourites {
		if n, ok := byID[id]; ok {
			f.FavouriteNames = append(f.FavouriteNames, n)
		}
	}
	for _, g := range Groups(chs, prefs) {
		row := LockRow{Key: strconv.FormatInt(g.ID, 10), Name: g.Name, Count: g.ChannelCount, Locked: prefs.Categories[g.ID].Locked}
		if g.Kind == store.KindRadio {
			f.Radio = append(f.Radio, row)
		} else {
			f.TV = append(f.TV, row)
		}
	}
	if vod != nil {
		if f.OnDemand, err = st.UserOnDemand(ctx, u.ID); err != nil {
			return f, err
		}
	}
	if f.OnDemand {
		for _, t := range []media.LibraryType{media.Movies, media.Shows} {
			for _, c := range vod.Categories(t) {
				ref := store.MediaRef{Kind: string(t), Name: c.Name}
				row := LockRow{Key: ref.Key(), Name: c.Name, Count: c.Items, Locked: prefs.Parental.MediaLocked(ref.Kind, ref.Name)}
				if t == media.Movies {
					f.Movies = append(f.Movies, row)
				} else {
					f.Shows = append(f.Shows, row)
				}
			}
		}
	}
	return f, nil
}

// SaveForm stores the locks ticked in a submitted form: "group"
// holds channel category ids, "media" On Demand category keys, "max_age"
// the age limit. Only groups the user has can be locked; locked On Demand
// categories no media server offers right now are kept.
func SaveForm(ctx context.Context, st *store.Store, vod *media.Catalog, u store.User, r *http.Request) error {
	chs, err := st.UserChannels(ctx, u.ID)
	if err != nil {
		return err
	}
	prefs, err := st.Preferences(ctx, u.ID)
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	want := map[int64]bool{}
	for _, v := range r.Form["group"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			want[id] = true
		}
	}
	for _, g := range store.ChannelGroups(chs) {
		if want[g.ID] != prefs.Categories[g.ID].Locked {
			if err := st.SetCategoryLocked(ctx, u.ID, g.ID, want[g.ID]); err != nil {
				return err
			}
		}
	}
	offered := map[string]bool{}
	if vod != nil {
		for _, t := range []media.LibraryType{media.Movies, media.Shows} {
			for _, c := range vod.Categories(t) {
				offered[store.MediaRef{Kind: string(t), Name: c.Name}.Key()] = true
			}
		}
	}
	var refs []store.MediaRef
	for _, m := range prefs.Parental.LockedMedia {
		if !offered[m.Key()] {
			refs = append(refs, m)
		}
	}
	for _, v := range r.Form["media"] {
		if m, ok := store.ParseMediaRef(v); ok && offered[m.Key()] {
			refs = append(refs, m)
		}
	}
	if err := st.SetLockedMedia(ctx, u.ID, refs); err != nil {
		return err
	}
	age, _ := strconv.Atoi(r.FormValue("max_age"))
	return st.SetMaxAge(ctx, u.ID, age)
}

// Summary describes in one line what is locked.
func Summary(lang string, f Form) string {
	count := func(rows ...[]LockRow) int {
		n := 0
		for _, list := range rows {
			for _, r := range list {
				if r.Locked {
					n++
				}
			}
		}
		return n
	}
	var parts []string
	switch n := count(f.TV, f.Radio); n {
	case 0:
	case 1:
		parts = append(parts, i18n.T(lang, "1 channel group locked"))
	default:
		parts = append(parts, i18n.Tf(lang, "%d channel groups locked", n))
	}
	switch n := count(f.Movies, f.Shows); n {
	case 0:
	case 1:
		parts = append(parts, i18n.T(lang, "1 On Demand category locked"))
	default:
		parts = append(parts, i18n.Tf(lang, "%d On Demand categories locked", n))
	}
	if f.MaxAge > 0 && f.OnDemand {
		parts = append(parts, i18n.Tf(lang, "On Demand up to %d years", f.MaxAge))
	}
	if len(parts) == 0 {
		return i18n.T(lang, "Nothing is locked yet.")
	}
	return strings.Join(parts, " · ")
}
