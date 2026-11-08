// Package alerts tells the admins when something needs attention, by
// email, Telegram or Discord: streams that stopped working, subscriptions
// that are about to expire and backups that failed. It also emails users a
// reminder before their own subscription expires.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Config says where alerts go and which ones are sent. A delivery channel
// is on when its fields are filled in.
type Config struct {
	// EmailTo are the admins who get alerts by email, through the mail
	// server of the settings.
	EmailTo []string `json:"email_to"`
	// TelegramToken is the token of a Telegram bot (from @BotFather) and
	// TelegramChat the chat, group or channel it posts to.
	TelegramToken string `json:"telegram_token,omitempty"`
	TelegramChat  string `json:"telegram_chat"`
	// DiscordWebhook is the webhook URL of a Discord channel.
	DiscordWebhook string `json:"discord_webhook,omitempty"`

	// DeadStreams alerts when a channel fails DeadAfter checks in a row,
	// and once more when it works again.
	DeadStreams bool `json:"dead_streams"`
	DeadAfter   int  `json:"dead_after"`
	// Expiring alerts ExpiringDays before a user's subscription ends.
	Expiring     bool `json:"expiring"`
	ExpiringDays int  `json:"expiring_days"`
	// BackupFailed alerts when a backup fails, and once more when backups
	// work again.
	BackupFailed bool `json:"backup_failed"`

	// Reminders emails users ReminderDays before their subscription
	// expires, one email per number of days.
	Reminders    bool  `json:"reminders"`
	ReminderDays []int `json:"reminder_days"`
	// ReminderContact tells users how to extend, e.g. an email address or
	// phone number; empty only says to contact the admin.
	ReminderContact string `json:"reminder_contact"`
}

// DefaultConfig sends every kind of alert once a channel is set up, and no
// reminders to users until an admin switches them on.
var DefaultConfig = Config{
	DeadStreams: true, DeadAfter: 3,
	Expiring: true, ExpiringDays: 7,
	BackupFailed: true,
	ReminderDays: []int{7, 1},
}

// Limits of the settings.
const (
	MaxDeadAfter    = 100
	MaxDays         = 90
	MaxReminderDays = 5
	MaxRecipients   = 10
)

// Delivery channels.
const (
	Email    = "email"
	Telegram = "telegram"
	Discord  = "discord"
)

// Channels lists the delivery channels that are set up.
func (c Config) Channels() []string {
	var out []string
	if len(c.EmailTo) > 0 {
		out = append(out, Email)
	}
	if c.TelegramToken != "" && c.TelegramChat != "" {
		out = append(out, Telegram)
	}
	if c.DiscordWebhook != "" {
		out = append(out, Discord)
	}
	return out
}

var (
	telegramToken = regexp.MustCompile(`^[0-9]{3,}:[A-Za-z0-9_-]{20,}$`)
	telegramChat  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{3,})$`)
)

// Validate checks and normalises the configuration.
func (c *Config) Validate() error {
	var to []string
	for _, a := range c.EmailTo {
		for _, a := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' }) {
			addr, err := mail.ParseAddress(a)
			if err != nil {
				return fmt.Errorf("%q is not a valid email address", a)
			}
			if !slices.Contains(to, addr) {
				to = append(to, addr)
			}
		}
	}
	if len(to) > MaxRecipients {
		return fmt.Errorf("enter at most %d email addresses", MaxRecipients)
	}
	c.EmailTo = to

	c.TelegramToken = strings.TrimSpace(c.TelegramToken)
	c.TelegramChat = strings.TrimSpace(c.TelegramChat)
	if c.TelegramToken != "" && !telegramToken.MatchString(c.TelegramToken) {
		return errors.New("the Telegram bot token looks like 123456789:AA… (get it from @BotFather)")
	}
	if c.TelegramChat != "" && !telegramChat.MatchString(c.TelegramChat) {
		return errors.New("the Telegram chat must be a chat ID such as -1001234567890 or a channel name such as @mychannel")
	}
	if (c.TelegramToken == "") != (c.TelegramChat == "") {
		return errors.New("enter both the Telegram bot token and the chat ID, or neither")
	}

	c.DiscordWebhook = strings.TrimSpace(c.DiscordWebhook)
	if c.DiscordWebhook != "" {
		u, err := url.Parse(c.DiscordWebhook)
		if err != nil || u.Scheme != "https" || u.Host == "" || !strings.Contains(u.Path, "/api/webhooks/") {
			return errors.New("the Discord webhook must look like https://discord.com/api/webhooks/…")
		}
	}

	if c.DeadAfter < 1 || c.DeadAfter > MaxDeadAfter {
		return fmt.Errorf("failed checks before an alert must be between 1 and %d", MaxDeadAfter)
	}
	if c.ExpiringDays < 1 || c.ExpiringDays > MaxDays {
		return fmt.Errorf("days before expiry must be between 1 and %d", MaxDays)
	}
	var days []int
	for _, d := range c.ReminderDays {
		if d < 1 || d > MaxDays {
			return fmt.Errorf("reminder days must be between 1 and %d", MaxDays)
		}
		if !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	if len(days) > MaxReminderDays {
		return fmt.Errorf("send at most %d reminders", MaxReminderDays)
	}
	if c.Reminders && len(days) == 0 {
		return errors.New("enter when the reminders are sent, for example 7, 1")
	}
	c.ReminderContact = strings.TrimSpace(c.ReminderContact)
	if strings.ContainsAny(c.ReminderContact, "\r\n") || len(c.ReminderContact) > 200 {
		return errors.New("the contact for reminders must be a single line of at most 200 characters")
	}
	slices.SortFunc(days, func(a, b int) int { return b - a })
	c.ReminderDays = days
	return nil
}

// ParseDays reads a list of numbers of days such as "7, 1".
func ParseDays(s string) ([]int, error) {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number of days", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// DaysString writes days back as "7, 1".
func DaysString(days []int) string {
	s := make([]string, len(days))
	for i, d := range days {
		s[i] = strconv.Itoa(d)
	}
	return strings.Join(s, ", ")
}

const configKey = "alerts_config"

// LoadConfig reads the configuration from the database.
func LoadConfig(ctx context.Context, st *store.Store) (Config, error) {
	c := DefaultConfig
	c.ReminderDays = slices.Clone(DefaultConfig.ReminderDays)
	v, err := st.Value(ctx, configKey)
	if errors.Is(err, store.ErrNotFound) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(v), &c); err != nil {
		return DefaultConfig, fmt.Errorf("reading the alert settings: %w", err)
	}
	return c, nil
}

// SaveConfig validates and stores the configuration.
func SaveConfig(ctx context.Context, st *store.Store, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return st.SetValue(ctx, configKey, string(b))
}
