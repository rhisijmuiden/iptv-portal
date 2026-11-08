package alerts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/iptv-portal/internal/i18n"
	"github.com/rhisijmuiden/iptv-portal/internal/mail"
	"github.com/rhisijmuiden/iptv-portal/internal/store"
)

// Service sends the alerts and reminders.
type Service struct {
	store  *store.Store
	log    *slog.Logger
	client *http.Client
	// TelegramAPI is where Telegram is reached; changed by tests.
	TelegramAPI string
	// SendMail delivers email; changed by tests.
	SendMail func(context.Context, mail.Config, mail.Message) error
	now      func() time.Time

	mu sync.Mutex // one check at a time
}

// New returns the alert service for st.
func New(st *store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, log: log, client: &http.Client{Timeout: sendTimeout},
		TelegramAPI: DefaultTelegramAPI, SendMail: mail.Send, now: time.Now}
}

// Loop checks for expiring subscriptions every few minutes until ctx ends.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.CheckExpiry(ctx)
		if err := s.store.PruneAlerts(ctx, s.now().AddDate(0, 0, -180)); err != nil {
			s.log.Warn("pruning old alerts", "err", err)
		}
		t.Reset(10 * time.Minute)
	}
}

// Deliver sends a to every channel that is set up and logs it. It returns
// the channels it reached; the error lists the ones that failed.
func (s *Service) Deliver(ctx context.Context, c Config, st store.Settings, a Alert) ([]string, error) {
	return s.deliver(ctx, c, st, a, c.Channels())
}

func (s *Service) deliver(ctx context.Context, c Config, st store.Settings, a Alert, channels []string) ([]string, error) {
	lang := st.Language
	text := a.Text(func(n int) string { return i18n.Tf(lang, "… and %d more", n) })
	var sent []string
	var errs []error
	for _, ch := range channels {
		var err error
		switch ch {
		case Email:
			if !st.Mail.Configured() {
				err = errors.New("email: no mail server is set up in the settings")
				break
			}
			for _, to := range c.EmailTo {
				if e := s.SendMail(ctx, st.Mail, mail.AlertMessage(lang, to, st.PortalName, a.Title, text)); e != nil {
					err = fmt.Errorf("email to %s: %w", to, e)
				}
			}
		case Telegram:
			err = s.sendTelegram(ctx, c.TelegramToken, c.TelegramChat, text)
		case Discord:
			err = s.sendDiscord(ctx, c.DiscordWebhook, st.PortalName, text)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sent = append(sent, ch)
	}
	err := errors.Join(errs...)
	e := store.AlertLogEntry{At: s.now(), Kind: a.Kind, Title: a.Title, Detail: strings.Join(a.Lines, "\n"), Channels: sent}
	if err != nil {
		e.Error = err.Error()
		s.log.Warn("sending alert", "title", a.Title, "err", err)
	} else {
		s.log.Info("alert sent", "title", a.Title, "to", strings.Join(sent, ", "))
	}
	if lerr := s.store.LogAlert(ctx, e); lerr != nil {
		s.log.Warn("logging alert", "err", lerr)
	}
	return sent, err
}

// Test sends a test alert to one channel.
func (s *Service) Test(ctx context.Context, channel string) error {
	c, st, err := s.load(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(c.Channels(), channel) {
		return errors.New("set up this channel first")
	}
	a := Alert{Kind: KindTest, Title: i18n.Tf(st.Language, "Test alert from %s", st.PortalName),
		Lines: []string{i18n.T(st.Language, "Alerts reach you here.")}}
	_, err = s.deliver(ctx, c, st, a, []string{channel})
	return err
}

func (s *Service) load(ctx context.Context) (Config, store.Settings, error) {
	c, err := LoadConfig(ctx, s.store)
	if err != nil {
		return c, store.Settings{}, err
	}
	st, err := s.store.Settings(ctx)
	return c, st, err
}

const streamKey = "stream:"

// StreamsChecked is called after every stream check. It alerts about
// channels that started failing and channels that work again.
func (s *Service) StreamsChecked(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("reading alert settings", "err", err)
		return
	}
	if !c.DeadStreams || len(c.Channels()) == 0 {
		return
	}
	chs, err := s.store.Channels(ctx, store.ChannelFilter{})
	if err != nil {
		s.log.Warn("listing channels for alerts", "err", err)
		return
	}
	health, err := s.store.ChannelHealth(ctx)
	if err != nil {
		s.log.Warn("reading stream health for alerts", "err", err)
		return
	}
	keys, err := s.store.AlertKeys(ctx, streamKey)
	if err != nil {
		s.log.Warn("reading sent alerts", "err", err)
		return
	}
	alerted := map[string]bool{}
	for _, k := range keys {
		alerted[k] = true
	}
	lang := st.Language
	var down, up, mark, forget []string
	for _, ch := range chs {
		key := streamKey + strconv.FormatInt(ch.ID, 10)
		was := alerted[key]
		delete(alerted, key)
		h, ok := health[ch.ID]
		if !ok {
			continue
		}
		failing := !h.OK && h.Failures >= c.DeadAfter && (ch.Enabled || h.AutoDisabled)
		switch {
		case failing && !was:
			line := ch.Name
			if ch.IsRadio() {
				line = i18n.Tf(lang, "%s (radio)", ch.Name)
			}
			if h.Message != "" {
				line += ": " + h.Message
			}
			if h.AutoDisabled {
				line += " " + i18n.T(lang, "(switched off)")
			}
			down = append(down, line)
			mark = append(mark, key)
		case was && h.OK:
			up = append(up, ch.Name)
			forget = append(forget, key)
		case was && !ch.Enabled && !h.AutoDisabled:
			forget = append(forget, key) // switched off by an admin
		}
	}
	for k := range alerted {
		forget = append(forget, k) // channel deleted
	}
	if len(down) > 0 || len(up) > 0 {
		a := Alert{Kind: KindStreams}
		switch {
		case len(up) == 0 && len(down) == 1:
			a.Title = i18n.T(lang, "A stream stopped working")
		case len(up) == 0:
			a.Title = i18n.Tf(lang, "%d streams stopped working", len(down))
		case len(down) == 0 && len(up) == 1:
			a.Title = i18n.T(lang, "A stream works again")
		case len(down) == 0:
			a.Title = i18n.Tf(lang, "%d streams work again", len(up))
		default:
			a.Title = i18n.Tf(lang, "%d streams stopped working, %d work again", len(down), len(up))
		}
		a.Lines = down
		for _, n := range up {
			a.Lines = append(a.Lines, i18n.Tf(lang, "%s works again", n))
		}
		if sent, _ := s.Deliver(ctx, c, st, a); len(sent) == 0 {
			return // nothing went out; try again after the next check
		}
	}
	if err := s.store.MarkAlertsSent(ctx, mark...); err != nil {
		s.log.Warn("storing sent alerts", "err", err)
	}
	if err := s.store.ForgetAlerts(ctx, forget...); err != nil {
		s.log.Warn("storing sent alerts", "err", err)
	}
}

const backupKey = "backup:failed"

// BackupFinished is called after every backup with its error, nil when
// it worked. A failure is alerted once, until backups work again.
func (s *Service) BackupFinished(ctx context.Context, backupErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("reading alert settings", "err", err)
		return
	}
	was, err := s.store.AlertSent(ctx, backupKey)
	if err != nil {
		s.log.Warn("reading sent alerts", "err", err)
		return
	}
	if !c.BackupFailed || len(c.Channels()) == 0 {
		if was {
			s.store.ForgetAlerts(ctx, backupKey)
		}
		return
	}
	lang := st.Language
	switch {
	case backupErr != nil && !was:
		a := Alert{Kind: KindBackup, Title: i18n.T(lang, "A backup failed"), Lines: []string{backupErr.Error(),
			i18n.T(lang, "You get no new alert for failed backups until a backup works again.")}}
		if sent, _ := s.Deliver(ctx, c, st, a); len(sent) > 0 {
			s.store.MarkAlertsSent(ctx, backupKey)
		}
	case backupErr == nil && was:
		a := Alert{Kind: KindBackup, Title: i18n.T(lang, "Backups work again")}
		s.Deliver(ctx, c, st, a)
		s.store.ForgetAlerts(ctx, backupKey)
	}
}

// DaysLeft is the number of days until t, rounded, but at least 1.
func DaysLeft(now, t time.Time) int {
	return max(1, int((t.Sub(now)+12*time.Hour)/(24*time.Hour)))
}

func expiryID(u store.User) string {
	return strconv.FormatInt(u.ID, 10) + ":" + strconv.FormatInt(u.ExpiresAt.Unix(), 10)
}

// CheckExpiry alerts the admins about subscriptions that expire soon and
// emails the reminders to users. A new expiry date counts as a new
// subscription, so extending it starts the reminders over.
func (s *Service) CheckExpiry(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("reading alert settings", "err", err)
		return
	}
	if !(c.Expiring && len(c.Channels()) > 0) && !(c.Reminders && st.Mail.Configured()) {
		return
	}
	users, err := s.store.Users(ctx, "")
	if err != nil {
		s.log.Warn("listing users for alerts", "err", err)
		return
	}
	now := s.now()
	var soon []store.User
	for _, u := range users {
		if u.Enabled && u.ExpiresAt != nil && u.ExpiresAt.After(now) {
			soon = append(soon, u)
		}
	}
	slices.SortFunc(soon, func(a, b store.User) int { return a.ExpiresAt.Compare(*b.ExpiresAt) })
	if c.Expiring && len(c.Channels()) > 0 {
		s.alertExpiring(ctx, c, st, soon, now)
	}
	if c.Reminders && st.Mail.Configured() {
		s.remind(ctx, c, st, soon, now)
	}
}

func (s *Service) alertExpiring(ctx context.Context, c Config, st store.Settings, users []store.User, now time.Time) {
	lang := st.Language
	var lines, mark []string
	for _, u := range users {
		left := DaysLeft(now, *u.ExpiresAt)
		if left > c.ExpiringDays {
			continue
		}
		key := "expiring:" + expiryID(u)
		if sent, err := s.store.AlertSent(ctx, key); err != nil || sent {
			continue
		}
		when := i18n.LongDate(lang, u.ExpiresAt.Local())
		var line string
		if left <= 1 {
			line = i18n.Tf(lang, "%s expires within a day, on %s", u.Username, when)
		} else {
			line = i18n.Tf(lang, "%s expires in %d days, on %s", u.Username, left, when)
		}
		if u.Email == "" {
			line += " " + i18n.T(lang, "(no email address)")
		}
		lines = append(lines, line)
		mark = append(mark, key)
	}
	if len(lines) == 0 {
		return
	}
	a := Alert{Kind: KindExpiring, Lines: lines}
	if len(lines) == 1 {
		a.Title = i18n.T(lang, "A subscription expires soon")
	} else {
		a.Title = i18n.Tf(lang, "%d subscriptions expire soon", len(lines))
	}
	if sent, _ := s.Deliver(ctx, c, st, a); len(sent) == 0 {
		return
	}
	if err := s.store.MarkAlertsSent(ctx, mark...); err != nil {
		s.log.Warn("storing sent alerts", "err", err)
	}
}

// remind emails each user once per reminder day that has been reached.
// When several are reached at once (a user created two days before the
// end), only the nearest one is sent.
func (s *Service) remind(ctx context.Context, c Config, st store.Settings, users []store.User, now time.Time) {
	for _, u := range users {
		if u.Email == "" {
			continue
		}
		left := DaysLeft(now, *u.ExpiresAt)
		var due []string
		send := false
		for _, d := range c.ReminderDays { // largest first
			if left > d {
				continue
			}
			key := "reminder:" + expiryID(u) + ":" + strconv.Itoa(d)
			due = append(due, key)
			if sent, err := s.store.AlertSent(ctx, key); err == nil && !sent {
				send = true
			}
		}
		if !send {
			continue
		}
		r := mail.Reminder{PortalName: st.PortalName, Username: u.Username, ExpiresAt: *u.ExpiresAt, Contact: c.ReminderContact}
		if st.AccountPortal && st.PublicURL != "" {
			r.AccountURL = st.PublicURL + "/account"
		}
		if err := s.SendMail(ctx, st.Mail, mail.ReminderMessage(st.Language, u.Email, r, left)); err != nil {
			s.log.Warn("emailing expiry reminder", "username", u.Username, "err", err)
			continue
		}
		s.log.Info("emailed expiry reminder", "username", u.Username, "to", u.Email, "days", left)
		if err := s.store.MarkAlertsSent(ctx, due...); err != nil {
			s.log.Warn("storing sent reminders", "err", err)
		}
	}
}
