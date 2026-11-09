package portal

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/rhisijmuiden/iptv-portal/internal/prefs"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
	"github.com/rhisijmuiden/iptv-portal/internal/web"
)

// The "My channels" page lets users pick favourite channels, put the
// channel groups in their own order and hide groups they never watch.

func (h *Handler) myChannelsRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.UserSession))) {
	auth("GET /account/channels", h.accountChannels)
	auth("POST /account/channels/favourite", h.accountFavourite)
	auth("POST /account/channels/favourites/move", h.accountFavouriteMove)
	auth("POST /account/channels/favourites/group", h.accountFavouritesGroup)
	auth("POST /account/channels/groups/move", h.accountGroupMove)
	auth("POST /account/channels/groups/hide", h.accountGroupHide)
	auth("POST /account/channels/groups/reset", h.accountGroupReset)
}

// channelRow is a channel on the My channels page.
type channelRow struct {
	store.Channel
	Logo      string
	Favourite bool
}

// groupRow is a channel group on the My channels page.
type groupRow struct {
	store.Category
	Hidden, Locked bool
	First, Last    bool // first or last of its kind, so it cannot move further
	Channels       []channelRow
}

type myChannelsData struct {
	FavouritesGroup bool
	Favourites      []channelRow
	TV, Radio       []groupRow
	Other           []channelRow // TV channels without a group
	Open            int64        // the group to show opened
	Ordered         bool         // the user changed the order of groups
}

func (h *Handler) myChannelsData(ctx context.Context, r *http.Request, u store.User) (myChannelsData, error) {
	chs, err := h.store.UserChannels(ctx, u.ID)
	if err != nil {
		return myChannelsData{}, err
	}
	p, err := h.store.Preferences(ctx, u.ID)
	if err != nil {
		return myChannelsData{}, err
	}
	st, err := h.store.Settings(ctx)
	if err != nil {
		return myChannelsData{}, err
	}
	base := web.BaseURL(st.PublicURL, r)
	row := func(c store.Channel) channelRow {
		return channelRow{Channel: c, Logo: logoURL(base, c), Favourite: p.IsFavourite(c.ID)}
	}
	d := myChannelsData{FavouritesGroup: p.FavouritesGroup}
	d.Open, _ = strconv.ParseInt(r.FormValue("open"), 10, 64)
	byID := map[int64]store.Channel{}
	byGroup := map[int64][]channelRow{}
	for _, c := range chs {
		byID[c.ID] = c
		if c.CategoryID == 0 {
			if !c.IsRadio() {
				d.Other = append(d.Other, row(c))
			}
			continue
		}
		byGroup[c.CategoryID] = append(byGroup[c.CategoryID], row(c))
	}
	for _, id := range p.Favourites {
		if c, ok := byID[id]; ok {
			d.Favourites = append(d.Favourites, row(c))
		}
	}
	for _, g := range prefs.Groups(chs, p) {
		uc := p.Categories[g.ID]
		d.Ordered = d.Ordered || uc.Position > 0
		gr := groupRow{Category: g, Hidden: uc.Hidden, Locked: uc.Locked, Channels: byGroup[g.ID]}
		if g.Kind == store.KindRadio {
			d.Radio = append(d.Radio, gr)
		} else {
			d.TV = append(d.TV, gr)
		}
	}
	for _, list := range [][]groupRow{d.TV, d.Radio} {
		if len(list) > 0 {
			list[0].First, list[len(list)-1].Last = true, true
		}
	}
	return d, nil
}

func (h *Handler) accountChannels(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	d, err := h.myChannelsData(r.Context(), r, sess.User)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.renderAccount(w, r, "channels", http.StatusOK, accountPage{Title: "My channels", Nav: "channels", Session: &sess, Data: d})
}

// backToChannels returns to the My channels page at the given anchor,
// with group open opened.
func backToChannels(w http.ResponseWriter, r *http.Request, open int64, anchor, msg string) {
	q := "?ok=" + url.QueryEscape(msg)
	if open > 0 {
		q += "&open=" + strconv.FormatInt(open, 10)
	}
	if anchor != "" {
		q += "#" + anchor
	}
	http.Redirect(w, r, "/account/channels"+q, http.StatusSeeOther)
}

func formInt(r *http.Request, name string) int64 {
	n, _ := strconv.ParseInt(r.FormValue(name), 10, 64)
	return n
}

func (h *Handler) accountFavourite(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	id := formInt(r, "channel")
	chs, err := h.store.UserChannels(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	i := slices.IndexFunc(chs, func(c store.Channel) bool { return c.ID == id })
	on := r.FormValue("on") == "1"
	if i < 0 && on {
		http.NotFound(w, r)
		return
	}
	if err := h.store.SetFavourite(ctx, sess.User.ID, id, on); err != nil {
		h.serverError(w, err)
		return
	}
	msg := "Removed from your favourites."
	if on {
		msg = "Added to your favourites."
	}
	if r.FormValue("from") == "favourites" {
		backToChannels(w, r, 0, "favourites", msg)
		return
	}
	var group int64
	if i >= 0 {
		group = chs[i].CategoryID
	}
	backToChannels(w, r, group, "group-"+strconv.FormatInt(group, 10), msg)
}

// moved returns ids with the item id moved one place up (dir < 0) or
// down.
func moved(ids []int64, id int64, dir int) []int64 {
	i := slices.Index(ids, id)
	j := i + dir
	if i < 0 || j < 0 || j >= len(ids) {
		return ids
	}
	ids = slices.Clone(ids)
	ids[i], ids[j] = ids[j], ids[i]
	return ids
}

func direction(r *http.Request) int {
	if r.FormValue("dir") == "up" {
		return -1
	}
	return 1
}

func (h *Handler) accountFavouriteMove(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	p, err := h.store.Preferences(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if err := h.store.SetFavouriteOrder(ctx, sess.User.ID, moved(p.Favourites, formInt(r, "channel"), direction(r))); err != nil {
		h.serverError(w, err)
		return
	}
	backToChannels(w, r, 0, "favourites", "The order of your favourites has been saved.")
}

func (h *Handler) accountFavouritesGroup(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	if err := h.store.SetFavouritesGroup(r.Context(), sess.User.ID, r.FormValue("on") == "on"); err != nil {
		h.serverError(w, err)
		return
	}
	backToChannels(w, r, 0, "favourites", "Saved. Refresh the channel list in your player to see the change.")
}

func (h *Handler) accountGroupMove(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	chs, err := h.store.UserChannels(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	p, err := h.store.Preferences(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	id := formInt(r, "category")
	// Move within the groups of the same kind; TV stays before radio.
	var tv, radio []int64
	for _, g := range prefs.Groups(chs, p) {
		if g.Kind == store.KindRadio {
			radio = append(radio, g.ID)
		} else {
			tv = append(tv, g.ID)
		}
	}
	tv, radio = moved(tv, id, direction(r)), moved(radio, id, direction(r))
	if err := h.store.SetCategoryOrder(ctx, sess.User.ID, append(tv, radio...)); err != nil {
		h.serverError(w, err)
		return
	}
	backToChannels(w, r, 0, "group-"+strconv.FormatInt(id, 10), "The order of your channel groups has been saved.")
}

func (h *Handler) accountGroupHide(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	ctx := r.Context()
	id := formInt(r, "category")
	chs, err := h.store.UserChannels(ctx, sess.User.ID)
	if err != nil {
		h.serverError(w, err)
		return
	}
	if !slices.ContainsFunc(chs, func(c store.Channel) bool { return c.CategoryID == id && id != 0 }) {
		http.NotFound(w, r)
		return
	}
	hide := r.FormValue("hidden") == "1"
	if err := h.store.SetCategoryHidden(ctx, sess.User.ID, id, hide); err != nil {
		h.serverError(w, err)
		return
	}
	msg := "The group is shown in your player again."
	if hide {
		msg = "The group is hidden from your player."
	}
	backToChannels(w, r, 0, "group-"+strconv.FormatInt(id, 10), msg)
}

func (h *Handler) accountGroupReset(w http.ResponseWriter, r *http.Request, sess store.UserSession) {
	if err := h.store.SetCategoryOrder(r.Context(), sess.User.ID, nil); err != nil {
		h.serverError(w, err)
		return
	}
	backToChannels(w, r, 0, "groups", "The channel groups are back in their standard order.")
}
