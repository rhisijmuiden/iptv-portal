package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

const token = "123456789:AAbbccddeeffgghhiijjkkllmmnnoopp"

// fakes records what reached Telegram, Discord and the mail server.
type fakes struct {
	mu       sync.Mutex
	telegram []map[string]any
	discord  []map[string]any
	mails    []mail.Message
	fail     bool
}

func (f *fakes) texts(list []map[string]any, key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range list {
		out = append(out, m[key].(string))
	}
	return out
}

func setup(t *testing.T, c Config) (*Service, *store.Store, *fakes) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fakes{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok":false,"description":"Unauthorized","message":"Invalid Webhook Token"}`))
			return
		}
		switch {
		case r.URL.Path == "/bot"+token+"/sendMessage":
			f.telegram = append(f.telegram, m)
			w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/api/webhooks/"):
			f.discord = append(f.discord, m)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(st, nil)
	s.TelegramAPI = srv.URL
	s.client = srv.Client()
	s.SendMail = func(_ context.Context, _ mail.Config, m mail.Message) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mails = append(f.mails, m)
		return nil
	}
	ctx := context.Background()
	if c.DiscordWebhook == "discord" {
		c.DiscordWebhook = strings.Replace(srv.URL, "http://", "https://", 1) + "/api/webhooks/1/abc"
		// The test server speaks plain HTTP; Validate wants https.
		if err := SaveConfig(ctx, st, c); err != nil {
			t.Fatal(err)
		}
		c.DiscordWebhook = srv.URL + "/api/webhooks/1/abc"
		b, _ := json.Marshal(c)
		st.SetValue(ctx, configKey, string(b))
	} else if err := SaveConfig(ctx, st, c); err != nil {
		t.Fatal(err)
	}
	settings, _ := st.Settings(ctx)
	settings.PortalName = "Test TV"
	settings.PublicURL = "https://tv.example.com"
	settings.Mail = mail.Config{Host: "mail.example.com", Port: 587, Security: mail.StartTLS, From: "tv@example.com"}
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	return s, st, f
}

func withDefaults(c Config) Config {
	d := DefaultConfig
	d.EmailTo, d.TelegramToken, d.TelegramChat, d.DiscordWebhook = c.EmailTo, c.TelegramToken, c.TelegramChat, c.DiscordWebhook
	return d
}

func TestValidate(t *testing.T) {
	good := withDefaults(Config{EmailTo: []string{"a@example.com, b@example.com;a@example.com"},
		TelegramToken: token, TelegramChat: "-1001234567890", DiscordWebhook: "https://discord.com/api/webhooks/1/x"})
	good.ReminderDays = []int{1, 14, 7, 7}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(good.EmailTo, " ") != "a@example.com b@example.com" || DaysString(good.ReminderDays) != "14, 7, 1" {
		t.Errorf("normalised to %v %v", good.EmailTo, good.ReminderDays)
	}
	if got := good.Channels(); strings.Join(got, " ") != "email telegram discord" {
		t.Errorf("channels %v", got)
	}
	for name, c := range map[string]Config{
		"bad email":     {EmailTo: []string{"nope"}},
		"bad token":     {TelegramToken: "abc", TelegramChat: "1"},
		"chat only":     {TelegramChat: "12345"},
		"bad chat":      {TelegramToken: token, TelegramChat: "my chat"},
		"http webhook":  {DiscordWebhook: "http://discord.com/api/webhooks/1/x"},
		"other webhook": {DiscordWebhook: "https://example.com/hook"},
	} {
		c = withDefaults(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c := withDefaults(Config{})
	c.ReminderDays = []int{0}
	if err := c.Validate(); err == nil {
		t.Error("0 reminder days accepted")
	}
	c.Reminders, c.ReminderDays = true, nil
	if err := c.Validate(); err == nil {
		t.Error("reminders without days accepted")
	}
	if _, err := ParseDays("7, x"); err == nil {
		t.Error("ParseDays accepted a word")
	}
}

func TestDeadStreams(t *testing.T) {
	s, st, f := setup(t, withDefaults(Config{TelegramToken: token, TelegramChat: "42", DiscordWebhook: "discord"}))
	ctx := context.Background()
	nh, _ := st.SaveChannel(ctx, store.Channel{Name: "NH Nieuws", StreamURL: "https://example.com/nh.m3u8", Enabled: true})
	off, _ := st.SaveChannel(ctx, store.Channel{Name: "Switched off", StreamURL: "https://example.com/off.m3u8"})
	for range 2 {
		st.RecordHealth(ctx, nh, false, "HTTP 404", 0)
		st.RecordHealth(ctx, off, false, "HTTP 404", 0)
		s.StreamsChecked(ctx)
	}
	if n := len(f.texts(f.telegram, "text")); n != 0 {
		t.Fatalf("%d alerts before 3 failed checks", n)
	}
	st.RecordHealth(ctx, nh, false, "HTTP 404", 0)
	st.RecordHealth(ctx, off, false, "HTTP 404", 0)
	s.StreamsChecked(ctx)
	s.StreamsChecked(ctx) // no new failures: no new alert
	got := f.texts(f.telegram, "text")
	if len(got) != 1 || !strings.Contains(got[0], "A stream stopped working") || !strings.Contains(got[0], "NH Nieuws: HTTP 404") ||
		strings.Contains(got[0], "Switched off") {
		t.Fatalf("telegram got %q", got)
	}
	if d := f.texts(f.discord, "content"); len(d) != 1 || d[0] != got[0] {
		t.Errorf("discord got %q", d)
	}
	if f.discord[0]["username"] != "Test TV" || f.discord[0]["allowed_mentions"] == nil {
		t.Errorf("discord payload %v", f.discord[0])
	}
	st.RecordHealth(ctx, nh, true, "ok", 0)
	s.StreamsChecked(ctx)
	got = f.texts(f.telegram, "text")
	if len(got) != 2 || !strings.Contains(got[1], "A stream works again") || !strings.Contains(got[1], "NH Nieuws works again") {
		t.Fatalf("after recovery %q", got)
	}
	log, _ := st.AlertLog(ctx, 10)
	if len(log) != 2 || log[0].Kind != KindStreams || strings.Join(log[0].Channels, " ") != "telegram discord" || log[0].Error != "" {
		t.Errorf("log %+v", log)
	}
}

func TestNothingSentIsRetried(t *testing.T) {
	s, st, f := setup(t, withDefaults(Config{TelegramToken: token, TelegramChat: "42"}))
	ctx := context.Background()
	id, _ := st.SaveChannel(ctx, store.Channel{Name: "RTV", StreamURL: "https://example.com/rtv.m3u8", Enabled: true})
	for range 3 {
		st.RecordHealth(ctx, id, false, "timeout", 0)
	}
	f.fail = true
	s.StreamsChecked(ctx)
	log, _ := st.AlertLog(ctx, 10)
	if len(log) != 1 || !strings.Contains(log[0].Error, "Unauthorized") || strings.Contains(log[0].Error, token) {
		t.Fatalf("log %+v", log)
	}
	f.fail = false
	s.StreamsChecked(ctx)
	if got := f.texts(f.telegram, "text"); len(got) != 1 {
		t.Fatalf("not retried: %q", got)
	}
}

func TestSecretsStayOutOfErrors(t *testing.T) {
	s, _, _ := setup(t, withDefaults(Config{}))
	s.TelegramAPI = "http://127.0.0.1:1"
	err := s.sendTelegram(context.Background(), token, "42", "hi")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("error %v", err)
	}
	err = s.sendDiscord(context.Background(), "http://127.0.0.1:1/api/webhooks/1/secret-part", "TV", "hi")
	if err == nil || strings.Contains(err.Error(), "secret-part") {
		t.Errorf("error %v", err)
	}
}

func TestBackupAlerts(t *testing.T) {
	s, st, f := setup(t, withDefaults(Config{EmailTo: []string{"admin@example.com"}}))
	ctx := context.Background()
	s.BackupFinished(ctx, errors.New("connecting to nas: refused"))
	s.BackupFinished(ctx, errors.New("connecting to nas: refused"))
	s.BackupFinished(ctx, nil)
	s.BackupFinished(ctx, nil)
	if len(f.mails) != 2 {
		t.Fatalf("%d mails: %+v", len(f.mails), f.mails)
	}
	if m := f.mails[0]; m.To != "admin@example.com" || m.Subject != "[Test TV] A backup failed" || !strings.Contains(m.Body, "refused") {
		t.Errorf("first mail %+v", m)
	}
	if m := f.mails[1]; m.Subject != "[Test TV] Backups work again" {
		t.Errorf("second mail %+v", m)
	}
	// Switched off: nothing is sent.
	c, _ := LoadConfig(ctx, st)
	c.BackupFailed = false
	SaveConfig(ctx, st, c)
	s.BackupFinished(ctx, errors.New("x"))
	if len(f.mails) != 2 {
		t.Error("alert sent while switched off")
	}
}

func TestExpiry(t *testing.T) {
	c := withDefaults(Config{TelegramToken: token, TelegramChat: "42"})
	c.Reminders, c.ReminderContact = true, "help@example.com"
	s, st, f := setup(t, c)
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	exp := now.Add(5*24*time.Hour + time.Hour)
	uid, err := st.SaveUser(ctx, store.User{Username: "jan", Email: "jan@example.com", Enabled: true, ExpiresAt: &exp}, "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	far := now.AddDate(0, 2, 0)
	st.SaveUser(ctx, store.User{Username: "piet", Email: "piet@example.com", Enabled: true, ExpiresAt: &far}, "secret-password")
	never := store.User{Username: "kees", Enabled: true}
	st.SaveUser(ctx, never, "secret-password")

	s.CheckExpiry(ctx)
	s.CheckExpiry(ctx)
	tg := f.texts(f.telegram, "text")
	if len(tg) != 1 || !strings.Contains(tg[0], "A subscription expires soon") || !strings.Contains(tg[0], "jan expires in 5 days") {
		t.Fatalf("admin alerts %q", tg)
	}
	if len(f.mails) != 1 || f.mails[0].To != "jan@example.com" || !strings.Contains(f.mails[0].Body, "expires in 5 days") ||
		!strings.Contains(f.mails[0].Body, "help@example.com") || !strings.Contains(f.mails[0].Body, "https://tv.example.com/account") {
		t.Fatalf("reminders %+v", f.mails)
	}

	// The day before: the second reminder.
	now = exp.Add(-20 * time.Hour)
	s.CheckExpiry(ctx)
	s.CheckExpiry(ctx)
	if len(f.mails) != 2 || !strings.Contains(f.mails[1].Body, "expires within a day") {
		t.Fatalf("second reminder %+v", f.mails)
	}
	if len(f.texts(f.telegram, "text")) != 1 {
		t.Error("admin alerted twice for the same expiry")
	}

	// Extended, then close to the new end again: everything starts over,
	// but only the nearest reminder is sent.
	u, _ := st.User(ctx, uid)
	newExp := now.Add(12 * time.Hour)
	u.ExpiresAt = &newExp
	if _, err := st.SaveUser(ctx, u, ""); err != nil {
		t.Fatal(err)
	}
	s.CheckExpiry(ctx)
	if len(f.mails) != 3 || len(f.texts(f.telegram, "text")) != 2 {
		t.Fatalf("after extending: %d mails, %d alerts", len(f.mails), len(f.texts(f.telegram, "text")))
	}

	// Expired users and disabled reminders get nothing.
	now = newExp.Add(time.Hour)
	s.CheckExpiry(ctx)
	if len(f.mails) != 3 {
		t.Error("expired user got a reminder")
	}
}

func TestTest(t *testing.T) {
	s, _, f := setup(t, withDefaults(Config{TelegramToken: token, TelegramChat: "@mychannel"}))
	ctx := context.Background()
	if err := s.Test(ctx, Discord); err == nil {
		t.Error("test to a channel that is not set up")
	}
	if err := s.Test(ctx, Telegram); err != nil {
		t.Fatal(err)
	}
	if got := f.texts(f.telegram, "text"); len(got) != 1 || !strings.HasPrefix(got[0], "Test alert from Test TV") {
		t.Errorf("got %q", got)
	}
	if f.telegram[0]["chat_id"] != "@mychannel" {
		t.Errorf("chat %v", f.telegram[0]["chat_id"])
	}
}

func TestAlertText(t *testing.T) {
	a := Alert{Title: "T"}
	for range 30 {
		a.Lines = append(a.Lines, "x")
	}
	text := a.Text(func(n int) string { return "more " + string(rune('0'+n)) })
	if strings.Count(text, "• x") != maxLines || !strings.HasSuffix(text, "more 5") {
		t.Errorf("text %q", text)
	}
	if cut("ééé", 2) != "é…" {
		t.Error("cut splits characters wrongly")
	}
}
