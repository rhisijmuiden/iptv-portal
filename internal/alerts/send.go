package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Alert is one message to the admins.
type Alert struct {
	Kind  string // KindStreams, KindExpiring, KindBackup or KindTest
	Title string
	Lines []string
}

// Kinds of alerts.
const (
	KindStreams  = "streams"
	KindExpiring = "expiring"
	KindBackup   = "backup"
	KindTest     = "test"
)

// maxLines is the most lines in one alert; the rest is summarised.
const maxLines = 25

// Text is the alert as plain text.
func (a Alert) Text(more func(n int) string) string {
	var b strings.Builder
	b.WriteString(a.Title)
	if len(a.Lines) > 0 {
		b.WriteString("\n")
	}
	for i, l := range a.Lines {
		if i == maxLines {
			b.WriteString("\n" + more(len(a.Lines)-maxLines))
			break
		}
		b.WriteString("\n• " + l)
	}
	return b.String()
}

// DefaultTelegramAPI is where Telegram bots are reached.
const DefaultTelegramAPI = "https://api.telegram.org"

const sendTimeout = 20 * time.Second

// postJSON sends v to u. The URLs of Telegram and Discord hold secrets,
// so they are left out of the errors.
func (s *Service) postJSON(ctx context.Context, u string, v any) (*http.Response, []byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("invalid address")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp, b, nil
}

// sendTelegram posts text with the bot to the chat.
func (s *Service) sendTelegram(ctx context.Context, token, chat, text string) error {
	text = cut(text, 4000)
	resp, b, err := s.postJSON(ctx, strings.TrimRight(s.TelegramAPI, "/")+"/bot"+token+"/sendMessage", map[string]any{
		"chat_id": chat, "text": text, "disable_web_page_preview": true,
	})
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	json.Unmarshal(b, &r)
	if resp.StatusCode != http.StatusOK || !r.OK {
		if r.Description == "" {
			r.Description = resp.Status
		}
		return fmt.Errorf("telegram: %s", r.Description)
	}
	return nil
}

// sendDiscord posts text to the Discord webhook.
func (s *Service) sendDiscord(ctx context.Context, webhook, name, text string) error {
	text = cut(text, 1900)
	resp, b, err := s.postJSON(ctx, webhook, map[string]any{
		"username": name, "content": text,
		// Never ping @everyone or roles, whatever a channel name says.
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var r struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &r)
		if r.Message == "" {
			r.Message = resp.Status
		}
		return fmt.Errorf("discord: %s", r.Message)
	}
	return nil
}

// cut shortens s to at most n characters.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
