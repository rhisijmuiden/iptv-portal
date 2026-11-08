package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/rhisijmuiden/iptv-portal/internal/alerts"
)

func TestAlertsPage(t *testing.T) {
	h, st := newTestHandler(t, "")
	do := loggedIn(t, h, st)
	ctx := context.Background()
	const token = "123456789:AAbbccddeeffgghhiijjkkllmmnnoopp"

	w := do("GET", "/alerts", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "No alerts are sent yet") ||
		!strings.Contains(w.Body.String(), "No mail server is set up yet") {
		t.Fatalf("empty page: %d", w.Code)
	}

	form := url.Values{"email_to": {"admin@example.com"}, "telegram_token": {token}, "telegram_chat": {"-1001234567890"},
		"discord_webhook": {"https://discord.com/api/webhooks/1/secret"}, "dead_streams": {"on"}, "dead_after": {"2"},
		"expiring_days": {"10"}, "backup_failed": {"on"}, "reminders": {"on"}, "reminder_days": {"1, 14"},
		"reminder_contact": {"06 12345678"}, "action": {"save"}}
	if w := do("POST", "/alerts", form); w.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	c, _ := alerts.LoadConfig(ctx, st)
	if c.TelegramToken != token || c.DeadAfter != 2 || c.Expiring || c.ExpiringDays != 10 || alerts.DaysString(c.ReminderDays) != "14, 1" ||
		c.ReminderContact != "06 12345678" || len(c.Channels()) != 3 {
		t.Fatalf("stored %+v", c)
	}

	body := do("GET", "/alerts", nil).Body.String()
	if strings.Contains(body, token) || strings.Contains(body, "secret") {
		t.Error("a secret was sent to the browser")
	}
	if !strings.Contains(body, `value="14, 1"`) || !strings.Contains(body, "Stop sending alerts to Telegram") {
		t.Error("the page does not show the stored settings")
	}

	// Empty secret fields keep the stored ones; clearing removes them.
	form.Set("telegram_token", "")
	form.Set("discord_webhook", "")
	form.Set("clear_discord", "on")
	do("POST", "/alerts", form)
	c, _ = alerts.LoadConfig(ctx, st)
	if c.TelegramToken != token || c.DiscordWebhook != "" {
		t.Errorf("after saving without secrets: %+v", c)
	}

	form.Set("dead_after", "0")
	if w := do("POST", "/alerts", form); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "between 1 and 100") {
		t.Errorf("invalid value accepted: %d", w.Code)
	}

	// Testing email without a mail server explains why it failed.
	form.Set("dead_after", "3")
	form.Set("action", "test-email")
	if w := do("POST", "/alerts", form); !strings.Contains(w.Body.String(), "no mail server is set up") {
		t.Errorf("email test without a server: %d", w.Code)
	}
}
