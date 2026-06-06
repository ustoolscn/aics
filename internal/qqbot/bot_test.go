package qqbot

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aics/internal/feishu"
	"aics/internal/orchestrator"
)

func TestValidationSignature(t *testing.T) {
	got, err := validationSignature("DG5g3B4j9X2KOErG", "1725442341", "Arq0D5A61EgUu4OxUvOp")
	if err != nil {
		t.Fatal(err)
	}
	want := "87befc99c42c651b3aac0278e71ada338433ae26fcb24307bdc5ad38c1adc2d01bcfcadc0842edac85e85205028a1132afe09280305f13aa6909ffc2d652c706"
	if got != want {
		t.Fatalf("unexpected signature:\nwant %s\n got %s", want, got)
	}
}

func TestWebhookValidationResponse(t *testing.T) {
	bot := New("11111111", "DG5g3B4j9X2KOErG", "https://api.sgroup.qq.com", "/qq/events", false, nil, feishu.NewMemoryDeduper(time.Hour), slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/qq/events", strings.NewReader(`{"op":13,"d":{"plain_token":"Arq0D5A61EgUu4OxUvOp","event_ts":"1725442341"}}`))

	bot.WebhookHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["plain_token"] != "Arq0D5A61EgUu4OxUvOp" {
		t.Fatalf("unexpected plain token: %#v", body)
	}
	if body["signature"] == "" {
		t.Fatalf("missing signature: %#v", body)
	}
}

func TestParseC2CMessage(t *testing.T) {
	var payload callbackPayload
	if err := json.Unmarshal([]byte(`{
		"id":"evt-1",
		"op":0,
		"t":"C2C_MESSAGE_CREATE",
		"d":{
			"id":"msg-1",
			"openid":"user-openid",
			"author":{"user_openid":"author-openid"},
			"content":"hello <@!123>"
		}
	}`), &payload); err != nil {
		t.Fatal(err)
	}

	incoming, target, ok := parseMessageEvent(payload)
	if !ok {
		t.Fatal("expected message event")
	}
	if target.Kind != targetC2C || target.OpenID != "user-openid" {
		t.Fatalf("unexpected reply target: %#v", target)
	}
	if incoming.MessageID != "msg-1" || incoming.ChatID != "qq:c2c:user-openid" || incoming.ThreadID != "qq:c2c:user-openid" {
		t.Fatalf("unexpected incoming identity: %#v", incoming)
	}
	if incoming.SenderID != "author-openid" || incoming.Text != "hello" {
		t.Fatalf("unexpected incoming content: %#v", incoming)
	}
}

func TestParseGroupAtMessage(t *testing.T) {
	var payload callbackPayload
	if err := json.Unmarshal([]byte(`{
		"id":"evt-2",
		"op":0,
		"t":"GROUP_AT_MESSAGE_CREATE",
		"d":{
			"id":"msg-2",
			"group_openid":"group-openid",
			"author":{"member_openid":"member-openid"},
			"content":"<@!bot> 查一下订单"
		}
	}`), &payload); err != nil {
		t.Fatal(err)
	}

	incoming, target, ok := parseMessageEvent(payload)
	if !ok {
		t.Fatal("expected group message event")
	}
	if target.Kind != targetGroup || target.GroupOpenID != "group-openid" {
		t.Fatalf("unexpected reply target: %#v", target)
	}
	if incoming.ChatID != "qq:group:group-openid" || incoming.ThreadID != "qq:group:group-openid" {
		t.Fatalf("unexpected incoming identity: %#v", incoming)
	}
	if incoming.SenderID != "member-openid" || incoming.Text != "查一下订单" {
		t.Fatalf("unexpected incoming content: %#v", incoming)
	}
}

func TestWebhookRepliesToC2CMessage(t *testing.T) {
	var tokenCalls int
	var replyPath string
	var replyAuth string
	var replyBody map[string]any
	openapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1",
				"expires_in":   "7200",
			})
		case "/v2/users/user-openid/messages":
			replyPath = r.URL.Path
			replyAuth = r.Header.Get("Authorization")
			if err := json.NewDecoder(r.Body).Decode(&replyBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "reply-1"})
		default:
			t.Fatalf("unexpected openapi path: %s", r.URL.Path)
		}
	}))
	defer openapi.Close()

	orch := &fakeOrchestrator{reply: "你好，我在。"}
	bot := New("appid", "secret", openapi.URL, "/qq/events", false, orch, feishu.NewMemoryDeduper(time.Hour), slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/qq/events", strings.NewReader(`{
		"id":"evt-1",
		"op":0,
		"t":"C2C_MESSAGE_CREATE",
		"d":{"id":"msg-1","openid":"user-openid","content":"你好"}
	}`))

	bot.WebhookHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if tokenCalls != 1 {
		t.Fatalf("expected one token call, got %d", tokenCalls)
	}
	if replyPath != "/v2/users/user-openid/messages" || replyAuth != "QQBot token-1" {
		t.Fatalf("unexpected reply request path/auth: %s %s", replyPath, replyAuth)
	}
	if replyBody["content"] != "你好，我在。" || replyBody["msg_type"] != float64(0) || replyBody["msg_id"] != "msg-1" || replyBody["msg_seq"] != float64(1) {
		t.Fatalf("unexpected reply body: %#v", replyBody)
	}
	if len(orch.messages) != 1 || orch.messages[0].Text != "你好" {
		t.Fatalf("orchestrator was not called correctly: %#v", orch.messages)
	}
}

func TestWebhookRepliesToGroupAtMessage(t *testing.T) {
	var replyPath string
	openapi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token-1", "expires_in": 7200})
		case "/v2/groups/group-openid/messages":
			replyPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "reply-1"})
		default:
			t.Fatalf("unexpected openapi path: %s", r.URL.Path)
		}
	}))
	defer openapi.Close()

	bot := New("appid", "secret", openapi.URL, "/qq/events", false, &fakeOrchestrator{reply: "群回复"}, feishu.NewMemoryDeduper(time.Hour), slog.Default())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/qq/events", strings.NewReader(`{
		"id":"evt-2",
		"op":0,
		"t":"GROUP_AT_MESSAGE_CREATE",
		"d":{"id":"msg-2","group_openid":"group-openid","content":"<@!bot> 帮忙"}
	}`))

	bot.WebhookHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if replyPath != "/v2/groups/group-openid/messages" {
		t.Fatalf("unexpected group reply path: %s", replyPath)
	}
}

type fakeOrchestrator struct {
	reply    string
	messages []orchestrator.IncomingMessage
}

func (f *fakeOrchestrator) Handle(ctx context.Context, incoming orchestrator.IncomingMessage) (string, error) {
	f.messages = append(f.messages, incoming)
	return f.reply, nil
}
