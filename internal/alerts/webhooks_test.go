package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lum1t4/wpx/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int, body, location string) *http.Response {
	header := http.Header{}
	if location != "" {
		header.Set("Location", location)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestWebhookSenderUsesFixedContractsAndBoundedPayloads(t *testing.T) {
	var requests []*http.Request
	var bodies [][]byte
	sender := WebhookSender{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		payload, _ := io.ReadAll(request.Body)
		requests = append(requests, request)
		bodies = append(bodies, payload)
		if request.URL.Host == "hooks.slack.com" {
			return response(http.StatusOK, "ok", ""), nil
		}
		return response(http.StatusOK, `{"ok":true,"result":{}}`, ""), nil
	})}
	slack := model.SlackAlertConfig{Enabled: true, WebhookURL: "https://hooks.slack.com/services/T000/B000/secret_token"}
	telegram := model.TelegramAlertConfig{Enabled: true, BotToken: "123456789:abcdefghijklmnopqrstuvwxyzABCDEFGHI", ChatID: "-100123456789"}
	if err := sender.SendSlack(context.Background(), slack, "subject", strings.Repeat("x", 20000)); err != nil {
		t.Fatal(err)
	}
	if err := sender.SendTelegram(context.Background(), telegram, "subject", strings.Repeat("y", 10000)); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	if requests[0].URL.Host != "hooks.slack.com" || requests[0].Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Slack request=%#v", requests[0])
	}
	var slackPayload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(bodies[0], &slackPayload); err != nil || len(slackPayload.Text) > 12000 {
		t.Fatalf("Slack payload length=%d err=%v", len(slackPayload.Text), err)
	}
	if requests[1].URL.Host != "api.telegram.org" || requests[1].URL.Path != "/bot"+telegram.BotToken+"/sendMessage" {
		t.Fatalf("Telegram URL=%s", requests[1].URL.Redacted())
	}
	values, err := url.ParseQuery(string(bodies[1]))
	if err != nil || values.Get("chat_id") != telegram.ChatID || len(values.Get("text")) > 4096 {
		t.Fatalf("Telegram payload invalid: %v", err)
	}
}

func TestWebhookSenderDoesNotFollowRedirectOrExposeSecrets(t *testing.T) {
	calls := 0
	sender := WebhookSender{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return response(http.StatusFound, "secret response body", "https://attacker.example/collect"), nil
	})}
	secret := "top_secret_token"
	err := sender.SendSlack(context.Background(), model.SlackAlertConfig{Enabled: true, WebhookURL: "https://hooks.slack.com/services/T000/B000/" + secret}, "subject", "sensitive body")
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "response") {
		t.Fatalf("secret-bearing error: %v", err)
	}
}

func TestWebhookSenderHonorsTimeoutAndRedactsTransportError(t *testing.T) {
	secret := "abcdefghijklmnopqrstuvwxyzABCDEFGHI"
	sender := WebhookSender{Timeout: 10 * time.Millisecond, Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, errors.New("request contained " + request.URL.String())
	})}
	err := sender.SendTelegram(context.Background(), model.TelegramAlertConfig{Enabled: true, BotToken: "123456789:" + secret, ChatID: "123"}, "subject", "body")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error=%v", err)
	}
}
