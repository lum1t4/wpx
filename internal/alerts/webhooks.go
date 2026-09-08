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
	"unicode/utf8"

	"github.com/lum1t4/wpx/internal/model"
)

type ChannelSender interface {
	SendSlack(context.Context, model.SlackAlertConfig, string, string) error
	SendTelegram(context.Context, model.TelegramAlertConfig, string, string) error
}

// WebhookSender accepts a transport rather than a client so redirects and
// deadlines remain enforced when tests or the application inject networking.
type WebhookSender struct {
	Transport http.RoundTripper
	Timeout   time.Duration
}

func (s WebhookSender) SendSlack(ctx context.Context, config model.SlackAlertConfig, subject, body string) error {
	if err := model.ValidateSlackAlertConfig(config); err != nil {
		return err
	}
	message := boundedUTF8(strings.TrimSpace(subject)+"\n"+strings.TrimSpace(body), 12000)
	payload, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: message})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return errors.New("create Slack alert request")
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req, "Slack", func(body []byte) bool { return strings.TrimSpace(string(body)) == "ok" })
}

func (s WebhookSender) SendTelegram(ctx context.Context, config model.TelegramAlertConfig, subject, body string) error {
	if err := model.ValidateTelegramAlertConfig(config); err != nil {
		return err
	}
	values := url.Values{}
	values.Set("chat_id", config.ChatID)
	values.Set("text", boundedUTF8(strings.TrimSpace(subject)+"\n"+strings.TrimSpace(body), 4096))
	values.Set("disable_web_page_preview", "true")
	endpoint := "https://api.telegram.org/bot" + config.BotToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return errors.New("create Telegram alert request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(req, "Telegram", func(body []byte) bool {
		var result struct {
			Ok bool `json:"ok"`
		}
		return json.Unmarshal(body, &result) == nil && result.Ok
	})
}

func (s WebhookSender) do(req *http.Request, channel string, accepted func([]byte) bool) error {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := s.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s alert request failed", channel)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s alert endpoint returned HTTP %d", channel, response.StatusCode)
	}
	if readErr != nil || len(body) > 16384 || !accepted(body) {
		return fmt.Errorf("%s alert endpoint rejected the message", channel)
	}
	return nil
}

func boundedUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
