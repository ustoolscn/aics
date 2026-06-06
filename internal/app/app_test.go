package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aics/internal/config"
)

func TestQQWebhookValidationRouteRequiresOnlySecret(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:         ":0",
		WebhookPath:      "/feishu/events",
		QQBotSecret:      "DG5g3B4j9X2KOErG",
		QQBotWebhookPath: "/qq/events",
		SkillsEnabled:    false,
	}
	app := New(cfg)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/qq/events", strings.NewReader(`{"op":13,"d":{"plain_token":"Arq0D5A61EgUu4OxUvOp","event_ts":"1725442341"}}`))

	app.server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"signature"`) {
		t.Fatalf("missing signature response: %s", rec.Body.String())
	}
}
