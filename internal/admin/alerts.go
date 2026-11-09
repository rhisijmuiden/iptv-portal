package admin

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/alerts"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// ---- alerts and reminders ----

type expiringUser struct {
	User     store.User
	DaysLeft int
}

type alertsData struct {
	Config alerts.Config
	// Days is Config.ReminderDays as typed in the form.
	Days string
	// Channels are the delivery channels that are set up.
	Channels []string
	// MailOK is set when a mail server is set up in the settings.
	MailOK bool
	// HasTelegram and HasDiscord say a secret is stored; it is never sent
	// to the browser.
	HasTelegram, HasDiscord bool
	// Expiring are the users whose subscription ends within ExpiringDays.
	Expiring []expiringUser
	Log      []store.AlertLogEntry
	Now      time.Time
}

func (h *Handler) alertRoutes(auth func(string, func(http.ResponseWriter, *http.Request, store.Session))) {
	auth("GET /alerts", h.alertsPage)
	auth("POST /alerts", h.alertsSave)
}

func (h *Handler) alertsPage(w http.ResponseWriter, r *http.Request, sess store.Session) {
	c, err := alerts.LoadConfig(r.Context(), h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	h.renderAlerts(w, r, sess, c, alerts.DaysString(c.ReminderDays), "")
}

func (h *Handler) renderAlerts(w http.ResponseWriter, r *http.Request, sess store.Session, c alerts.Config, days, errMsg string) {
	ctx := r.Context()
	st, err := h.store.Settings(ctx)
	if err != nil {
		h.serverError(w, err)
		return
	}
	stored, err := alerts.LoadConfig(ctx, h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	d := alertsData{Config: c, Days: days, Channels: stored.Channels(), MailOK: st.Mail.Configured(),
		HasTelegram: stored.TelegramToken != "", HasDiscord: stored.DiscordWebhook != "", Now: time.Now()}
	// The secrets stay on the server.
	d.Config.TelegramToken, d.Config.DiscordWebhook = "", ""
	users, err := h.store.Users(ctx, "")
	if err != nil {
		h.serverError(w, err)
		return
	}
	until := d.Now.AddDate(0, 0, max(c.ExpiringDays, 1))
	for _, u := range users {
		if u.Enabled && u.ExpiresAt != nil && u.ExpiresAt.After(d.Now) && !u.ExpiresAt.After(until) {
			d.Expiring = append(d.Expiring, expiringUser{u, alerts.DaysLeft(d.Now, *u.ExpiresAt)})
		}
	}
	slices.SortFunc(d.Expiring, func(a, b expiringUser) int { return a.User.ExpiresAt.Compare(*b.User.ExpiresAt) })
	if d.Log, err = h.store.AlertLog(ctx, 20); err != nil {
		h.serverError(w, err)
		return
	}
	h.render(w, r, "alerts", page{Title: "Alerts", Nav: "alerts", Session: &sess, Error: errMsg, Data: d})
}

func (h *Handler) alertsSave(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ctx := r.Context()
	old, err := alerts.LoadConfig(ctx, h.store)
	if err != nil {
		h.serverError(w, err)
		return
	}
	on := func(name string) bool { return r.FormValue(name) == "on" }
	num := func(name string) int {
		n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
		if err != nil {
			return -1 // reported by Validate
		}
		return n
	}
	c := alerts.Config{
		EmailTo:         []string{r.FormValue("email_to")},
		TelegramToken:   r.FormValue("telegram_token"),
		TelegramChat:    r.FormValue("telegram_chat"),
		DiscordWebhook:  r.FormValue("discord_webhook"),
		DeadStreams:     on("dead_streams"),
		DeadAfter:       num("dead_after"),
		Expiring:        on("expiring"),
		ExpiringDays:    num("expiring_days"),
		BackupFailed:    on("backup_failed"),
		Reminders:       on("reminders"),
		ReminderContact: r.FormValue("reminder_contact"),
	}
	// Stored secrets are never sent to the browser: empty fields keep them.
	if strings.TrimSpace(c.TelegramToken) == "" && !on("clear_telegram") && strings.TrimSpace(c.TelegramChat) != "" {
		c.TelegramToken = old.TelegramToken
	}
	if on("clear_telegram") {
		c.TelegramToken, c.TelegramChat = "", ""
	}
	if strings.TrimSpace(c.DiscordWebhook) == "" && !on("clear_discord") {
		c.DiscordWebhook = old.DiscordWebhook
	}
	days := r.FormValue("reminder_days")
	if c.ReminderDays, err = alerts.ParseDays(days); err == nil {
		err = alerts.SaveConfig(ctx, h.store, c)
	}
	if err != nil {
		h.renderAlerts(w, r, sess, c, days, err.Error())
		return
	}
	action := r.FormValue("action")
	channel, ok := strings.CutPrefix(action, "test-")
	if !ok {
		redirectFlash(w, r, "/alerts", "Alert settings saved.")
		return
	}
	tctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := h.alerts.Test(tctx, channel); err != nil {
		h.renderAlerts(w, r, sess, c, alerts.DaysString(c.ReminderDays), tr(r, "Saved, but the test alert could not be sent: %s", err.Error()))
		return
	}
	redirectFlash(w, r, "/alerts", tr(r, "Saved. A test alert was sent by %s.", tr(r, channelName(channel))))
}

// channelName is the English name of a delivery channel.
func channelName(ch string) string {
	switch ch {
	case alerts.Email:
		return "email"
	case alerts.Telegram:
		return "Telegram"
	case alerts.Discord:
		return "Discord"
	}
	return ch
}
