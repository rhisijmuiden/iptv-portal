package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Preferences are what a user sets for their own player on the account
// pages: favourite channels, their own order of the channel groups,
// groups they do not want to see, and parental controls. The admin can
// see and change them on the user's page.
type Preferences struct {
	// FavouritesGroup puts the favourites in a group of their own at the
	// top of the player's channel list.
	FavouritesGroup bool
	// Favourites are channel ids in the user's order.
	Favourites []int64
	// Categories holds the user's choices per channel category; a
	// category without an entry keeps its default place.
	Categories map[int64]UserCategory
	Parental   Parental
}

// UserCategory is what a user chose for one channel category.
type UserCategory struct {
	Position int  // 1 = first; 0 = default place
	Hidden   bool // left out of the player
	Locked   bool // parental control: hidden until unlocked with the PIN
}

// MediaRef names an On Demand category: kind is "movies" or "shows". On
// Demand categories are named rather than numbered because categories
// with the same name on several media servers are shown as one.
type MediaRef struct {
	Kind string
	Name string
}

// Key is the form value of a MediaRef, e.g. "movies:Horror".
func (m MediaRef) Key() string { return m.Kind + ":" + m.Name }

// ParseMediaRef reads a MediaRef from its Key.
func ParseMediaRef(s string) (MediaRef, bool) {
	kind, name, ok := strings.Cut(s, ":")
	name = strings.TrimSpace(name)
	if !ok || (kind != "movies" && kind != "shows") || name == "" || len(name) > 200 {
		return MediaRef{}, false
	}
	return MediaRef{Kind: kind, Name: name}, true
}

// Parental are the parental controls of a user. Locked channel groups and
// On Demand categories, and On Demand titles above the age limit, are left
// out of the player until someone unlocks them with the PIN on the
// account pages.
type Parental struct {
	PINSet bool
	// MaxAge is the highest age rating of On Demand titles shown; 0 means
	// no limit.
	MaxAge int
	// UnlockedUntil is when an unlock with the PIN ends; nil or in the past
	// means locked.
	UnlockedUntil *time.Time
	LockedMedia   []MediaRef
}

// Unlocked reports whether the locked content is unlocked at now.
func (p Parental) Unlocked(now time.Time) bool {
	return p.UnlockedUntil != nil && p.UnlockedUntil.After(now)
}

// MediaLocked reports whether the On Demand category is locked (ignoring
// an unlock).
func (p Parental) MediaLocked(kind, name string) bool {
	for _, m := range p.LockedMedia {
		if m.Kind == kind && strings.EqualFold(m.Name, name) {
			return true
		}
	}
	return false
}

// LockedCount is the number of locked channel groups.
func (p Preferences) LockedCount() int {
	n := 0
	for _, c := range p.Categories {
		if c.Locked {
			n++
		}
	}
	return n
}

// HasLocks reports whether any parental control is set.
func (p Preferences) HasLocks() bool {
	return p.LockedCount() > 0 || len(p.Parental.LockedMedia) > 0 || p.Parental.MaxAge > 0
}

// IsFavourite reports whether the channel is one of the favourites.
func (p Preferences) IsFavourite(channelID int64) bool {
	return slices.Contains(p.Favourites, channelID)
}

// Age limits offered for On Demand.
var AgeLimits = []int{6, 9, 12, 14, 16, 18}

// PIN errors.
var (
	ErrPINFormat = errors.New("the PIN must be 4 to 8 digits")
	ErrPINWrong  = errors.New("wrong PIN")
	ErrPINNeeded = errors.New("set a PIN first")
)

// ValidPIN checks the format of a parental control PIN.
func ValidPIN(pin string) error {
	if len(pin) < 4 || len(pin) > 8 {
		return ErrPINFormat
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return ErrPINFormat
		}
	}
	return nil
}

// Preferences returns the preferences of a user; a user who never set
// anything gets the defaults.
func (s *Store) Preferences(ctx context.Context, userID int64) (Preferences, error) {
	p := Preferences{FavouritesGroup: true, Categories: map[int64]UserCategory{}}
	var hash string
	var until sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT favourites_group, pin_hash, max_age, unlocked_until FROM user_preferences WHERE user_id = ?`, userID).
		Scan(&p.FavouritesGroup, &hash, &p.Parental.MaxAge, &until)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return p, err
	}
	p.Parental.PINSet = hash != ""
	if until.Valid {
		t := until.Time
		p.Parental.UnlockedUntil = &t
	}
	if p.Favourites, err = s.ids(ctx,
		`SELECT f.channel_id FROM user_favourites f JOIN channels ch ON ch.id = f.channel_id
		 WHERE f.user_id = ? ORDER BY f.position, f.rowid`, userID); err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT category_id, position, hidden, locked FROM user_categories WHERE user_id = ?`, userID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c UserCategory
		if err := rows.Scan(&id, &c.Position, &c.Hidden, &c.Locked); err != nil {
			return p, err
		}
		p.Categories[id] = c
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	mrows, err := s.db.QueryContext(ctx,
		`SELECT kind, name FROM user_locked_media WHERE user_id = ? ORDER BY kind, name`, userID)
	if err != nil {
		return p, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var m MediaRef
		if err := mrows.Scan(&m.Kind, &m.Name); err != nil {
			return p, err
		}
		p.Parental.LockedMedia = append(p.Parental.LockedMedia, m)
	}
	return p, mrows.Err()
}

// ensurePreferences makes sure the user has a preferences row.
func ensurePreferences(ctx context.Context, ex execer, userID int64) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO user_preferences(user_id) VALUES(?) ON CONFLICT(user_id) DO NOTHING`, userID)
	return err
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// updatePreferences sets one column of the user's preferences.
func (s *Store) updatePreferences(ctx context.Context, userID int64, set string, args ...any) error {
	if err := ensurePreferences(ctx, s.db, userID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE user_preferences SET `+set+` WHERE user_id = ?`, append(args, userID)...)
	return err
}

// SetFavouritesGroup turns the Favourites group in the player on or off.
func (s *Store) SetFavouritesGroup(ctx context.Context, userID int64, on bool) error {
	return s.updatePreferences(ctx, userID, `favourites_group = ?`, on)
}

// SetFavourite adds a channel to the end of the favourites, or removes it.
func (s *Store) SetFavourite(ctx context.Context, userID, channelID int64, on bool) error {
	if !on {
		_, err := s.db.ExecContext(ctx, `DELETE FROM user_favourites WHERE user_id = ? AND channel_id = ?`, userID, channelID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_favourites(user_id, channel_id, position)
		 VALUES(?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM user_favourites WHERE user_id = ?))
		 ON CONFLICT DO NOTHING`, userID, channelID, userID)
	return err
}

// SetFavouriteOrder stores the order of the favourites; ids not among the
// favourites are ignored, favourites not in ids keep their place after
// them.
func (s *Store) SetFavouriteOrder(ctx context.Context, userID int64, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE user_favourites SET position = ? WHERE user_id = ? AND channel_id = ?`,
			i+1, userID, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_favourites SET position = position + ? WHERE user_id = ? AND channel_id NOT IN (`+
		placeholders(len(ids))+`)`, append([]any{len(ids)}, append([]any{userID}, anyIDs(ids)...)...)...); err != nil {
		return err
	}
	return tx.Commit()
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func anyIDs(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// updateCategory sets one column of the user's choices for a category.
func (s *Store) updateCategory(ctx context.Context, userID, categoryID int64, col string, v any) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_categories(user_id, category_id, `+col+`) VALUES(?, ?, ?)
		 ON CONFLICT(user_id, category_id) DO UPDATE SET `+col+` = excluded.`+col, userID, categoryID, v)
	return err
}

// SetCategoryHidden hides a channel category from the user's player, or
// shows it again.
func (s *Store) SetCategoryHidden(ctx context.Context, userID, categoryID int64, hidden bool) error {
	return s.updateCategory(ctx, userID, categoryID, "hidden", hidden)
}

// SetCategoryLocked locks or unlocks a channel category with parental
// control.
func (s *Store) SetCategoryLocked(ctx context.Context, userID, categoryID int64, locked bool) error {
	return s.updateCategory(ctx, userID, categoryID, "locked", locked)
}

// SetCategoryOrder stores the user's order of channel categories: ids
// get positions 1, 2, ... An empty ids resets every category to its
// default place.
func (s *Store) SetCategoryOrder(ctx context.Context, userID int64, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(ids) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE user_categories SET position = 0 WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO user_categories(user_id, category_id, position) VALUES(?, ?, ?)
			 ON CONFLICT(user_id, category_id) DO UPDATE SET position = excluded.position`, userID, id, i+1); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetLockedMedia replaces the locked On Demand categories of a user.
func (s *Store) SetLockedMedia(ctx context.Context, userID int64, refs []MediaRef) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_locked_media WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, m := range refs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_locked_media(user_id, kind, name) VALUES(?, ?, ?) ON CONFLICT DO NOTHING`,
			userID, m.Kind, m.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetMaxAge sets the age limit for On Demand; 0 means no limit.
func (s *Store) SetMaxAge(ctx context.Context, userID int64, age int) error {
	if age != 0 && !slices.Contains(AgeLimits, age) {
		age = 0
	}
	return s.updatePreferences(ctx, userID, `max_age = ?`, age)
}

// SetParentalPIN sets or changes the parental control PIN.
func (s *Store) SetParentalPIN(ctx context.Context, userID int64, pin string) error {
	if err := ValidPIN(pin); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return s.updatePreferences(ctx, userID, `pin_hash = ?`, string(hash))
}

// CheckParentalPIN compares pin with the user's PIN.
func (s *Store) CheckParentalPIN(ctx context.Context, userID int64, pin string) error {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT pin_hash FROM user_preferences WHERE user_id = ?`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && hash == "") {
		return ErrPINNeeded
	}
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pin)) != nil {
		return ErrPINWrong
	}
	return nil
}

// RemoveParentalPIN removes the PIN (the admin does this when a user
// forgot it). The locks stay; whoever sets a new PIN can change them.
func (s *Store) RemoveParentalPIN(ctx context.Context, userID int64) error {
	if err := s.updatePreferences(ctx, userID, `pin_hash = '', unlocked_until = NULL`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE user_sessions SET pin_until = NULL WHERE user_id = ?`, userID)
	return err
}

// SetUnlockedUntil unlocks the locked content until t; nil locks it again.
func (s *Store) SetUnlockedUntil(ctx context.Context, userID int64, t *time.Time) error {
	return s.updatePreferences(ctx, userID, `unlocked_until = ?`, utcPtr(t))
}

// PINSessionTTL is how long the parental control settings stay open on
// the account pages after entering the PIN.
const PINSessionTTL = 15 * time.Minute

// SetSessionPIN marks an account session as having entered the PIN until
// t; nil closes the parental control settings again.
func (s *Store) SetSessionPIN(ctx context.Context, token string, t *time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE user_sessions SET pin_until = ? WHERE token = ?`, utcPtr(t), token)
	return err
}

// ChannelGroups returns the categories of chs in the order they first
// appear, with the number of channels in each. Channels without a
// category are left out.
func ChannelGroups(chs []Channel) []Category {
	var out []Category
	at := map[int64]int{}
	for _, c := range chs {
		if c.CategoryID == 0 {
			continue
		}
		i, ok := at[c.CategoryID]
		if !ok {
			i = len(out)
			at[c.CategoryID] = i
			out = append(out, Category{ID: c.CategoryID, Name: c.Category, Kind: c.Kind})
		}
		out[i].ChannelCount++
	}
	return out
}

// PlayerView is what a player gets of a user's channels once the
// preferences are applied.
type PlayerView struct {
	// Channels are listed in the player: TV first, then radio, each in the
	// user's order of groups. Hidden and locked groups are left out.
	Channels []Channel
	// Favourites are the favourites the player may show, in the user's
	// order.
	Favourites []Channel
	// Playable are all channels the user may open: everything except
	// locked groups. Hidden groups can still be played, e.g. from the
	// favourites.
	Playable map[int64]Channel
	// Locked is true while parental controls hide something.
	Locked bool
}

// Listed returns the channels a player gets, the favourites first; each
// channel once.
func (v PlayerView) Listed() []Channel {
	out := slices.Clone(v.Favourites)
	for _, c := range v.Channels {
		if !slices.ContainsFunc(v.Favourites, func(f Channel) bool { return f.ID == c.ID }) {
			out = append(out, c)
		}
	}
	return out
}

// Apply orders and filters chs, the channels of the user's packages in
// their default order, for the player.
func (p Preferences) Apply(chs []Channel, now time.Time) PlayerView {
	unlocked := p.Parental.Unlocked(now)
	v := PlayerView{Playable: map[int64]Channel{}}
	first := map[int64]int{} // default place of each category
	for i, c := range chs {
		uc := p.Categories[c.CategoryID]
		if c.CategoryID != 0 && uc.Locked && !unlocked {
			v.Locked = true
			continue
		}
		v.Playable[c.ID] = c
		if _, ok := first[c.CategoryID]; !ok {
			first[c.CategoryID] = i
		}
		if c.CategoryID != 0 && uc.Hidden {
			continue
		}
		v.Channels = append(v.Channels, c)
	}
	key := func(c Channel) int {
		if pos := p.Categories[c.CategoryID].Position; c.CategoryID != 0 && pos > 0 {
			return pos
		}
		return 1_000_000 + first[c.CategoryID]
	}
	slices.SortStableFunc(v.Channels, func(a, b Channel) int {
		if a.IsRadio() != b.IsRadio() {
			if a.IsRadio() {
				return 1
			}
			return -1
		}
		return key(a) - key(b)
	})
	for _, id := range p.Favourites {
		if c, ok := v.Playable[id]; ok {
			v.Favourites = append(v.Favourites, c)
		}
	}
	if p.Parental.MaxAge > 0 || len(p.Parental.LockedMedia) > 0 {
		v.Locked = v.Locked || !unlocked
	}
	return v
}
