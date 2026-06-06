package qqbot

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"aics/internal/feishu"
	"aics/internal/orchestrator"
)

const (
	callbackDispatch = 0
	callbackACK      = 12
	callbackValidate = 13

	eventC2CMessage     = "C2C_MESSAGE_CREATE"
	eventGroupAtMessage = "GROUP_AT_MESSAGE_CREATE"

	targetC2C   = "c2c"
	targetGroup = "group"
)

type Orchestrator interface {
	Handle(ctx context.Context, incoming orchestrator.IncomingMessage) (string, error)
}

type Bot struct {
	appID        string
	secret       string
	baseURL      string
	webhookPath  string
	logRaw       bool
	orchestrator Orchestrator
	deduper      feishu.Deduper
	logger       *slog.Logger
	httpClient   *http.Client
	tokenMu      sync.Mutex
	token        string
	tokenExpiry  time.Time
}

func New(appID string, secret string, baseURL string, webhookPath string, logRaw bool, orch Orchestrator, deduper feishu.Deduper, logger *slog.Logger) *Bot {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.sgroup.qq.com"
	}
	if strings.TrimSpace(webhookPath) == "" {
		webhookPath = "/qq/events"
	}
	if deduper == nil {
		deduper = feishu.NewMemoryDeduper(time.Hour)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Bot{
		appID:        strings.TrimSpace(appID),
		secret:       strings.TrimSpace(secret),
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		webhookPath:  webhookPath,
		logRaw:       logRaw,
		orchestrator: orch,
		deduper:      deduper,
		logger:       logger,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (b *Bot) WebhookPath() string {
	return b.webhookPath
}

func (b *Bot) WebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if b.logRaw {
			b.logger.Info("raw qqbot callback received", "body", truncate(bodyString(body), 2000))
		}

		var payload callbackPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch payload.Op {
		case callbackValidate:
			b.writeValidation(w, payload)
		case callbackDispatch:
			b.handleDispatch(w, r.Context(), payload)
		default:
			writeJSON(w, http.StatusOK, map[string]int{"op": callbackACK})
		}
	}
}

func (b *Bot) writeValidation(w http.ResponseWriter, payload callbackPayload) {
	var data validationPayload
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	signature, err := validationSignature(b.secret, data.EventTS.String(), data.PlainToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"plain_token": data.PlainToken,
		"signature":   signature,
	})
}

func (b *Bot) handleDispatch(w http.ResponseWriter, ctx context.Context, payload callbackPayload) {
	incoming, target, ok := parseMessageEvent(payload)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]int{"op": callbackACK})
		return
	}

	dedupeKey := incoming.MessageID
	if payload.ID != "" {
		dedupeKey = payload.ID + ":" + incoming.MessageID
	}
	firstSeen, err := b.deduper.Mark(ctx, dedupeKey)
	if err != nil {
		b.logger.Warn("qqbot message dedupe failed; continuing", "message_id", incoming.MessageID, "err", err)
	} else if !firstSeen {
		b.logger.Info("duplicate qqbot message ignored", "message_id", incoming.MessageID)
		writeJSON(w, http.StatusOK, map[string]int{"op": callbackACK})
		return
	}

	if b.orchestrator == nil {
		b.logger.Warn("qqbot orchestrator is not configured", "message_id", incoming.MessageID)
		writeJSON(w, http.StatusOK, map[string]int{"op": callbackACK})
		return
	}

	reply, err := b.orchestrator.Handle(ctx, incoming)
	if err != nil {
		b.logger.Error("handle qqbot message failed", "message_id", incoming.MessageID, "err", err)
		reply = "抱歉，我处理这条消息时遇到了问题，请稍后再试或转人工。"
	}
	if strings.TrimSpace(reply) != "" {
		if err := b.reply(ctx, target, incoming.MessageID, 1, reply); err != nil {
			b.logger.Error("qqbot reply failed", "message_id", incoming.MessageID, "err", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]int{"op": callbackACK})
}

func validationSignature(secret string, eventTS string, plainToken string) (string, error) {
	seed := secret
	for len(seed) < ed25519.SeedSize {
		seed = strings.Repeat(seed, 2)
	}
	seed = seed[:ed25519.SeedSize]
	reader := strings.NewReader(seed)
	_, privateKey, err := ed25519.GenerateKey(reader)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(privateKey, []byte(eventTS+plainToken))
	return hex.EncodeToString(signature), nil
}

func parseMessageEvent(payload callbackPayload) (orchestrator.IncomingMessage, replyTarget, bool) {
	switch payload.Type {
	case eventC2CMessage:
		msg, ok := parseCallbackMessage(payload.Data)
		if !ok || msg.ID == "" || msg.OpenID == "" {
			return orchestrator.IncomingMessage{}, replyTarget{}, false
		}
		text := sanitizeContent(msg.Content)
		return orchestrator.IncomingMessage{
			MessageID:     msg.ID,
			ChatID:        "qq:c2c:" + msg.OpenID,
			ThreadID:      "qq:c2c:" + msg.OpenID,
			RootMessageID: msg.ID,
			SenderID:      firstNonEmpty(msg.Author.UserOpenID, msg.Author.OpenID, msg.OpenID),
			Text:          text,
		}, replyTarget{Kind: targetC2C, OpenID: msg.OpenID}, strings.TrimSpace(text) != ""
	case eventGroupAtMessage:
		msg, ok := parseCallbackMessage(payload.Data)
		if !ok || msg.ID == "" || msg.GroupOpenID == "" {
			return orchestrator.IncomingMessage{}, replyTarget{}, false
		}
		text := sanitizeContent(msg.Content)
		return orchestrator.IncomingMessage{
			MessageID:     msg.ID,
			ChatID:        "qq:group:" + msg.GroupOpenID,
			ThreadID:      "qq:group:" + msg.GroupOpenID,
			RootMessageID: msg.ID,
			SenderID:      firstNonEmpty(msg.Author.MemberOpenID, msg.Author.UserOpenID, msg.Author.OpenID),
			Text:          text,
		}, replyTarget{Kind: targetGroup, GroupOpenID: msg.GroupOpenID}, strings.TrimSpace(text) != ""
	default:
		return orchestrator.IncomingMessage{}, replyTarget{}, false
	}
}

func parseCallbackMessage(data json.RawMessage) (callbackMessage, bool) {
	var msg callbackMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return callbackMessage{}, false
	}
	return msg, true
}

func sanitizeContent(content string) string {
	content = strings.TrimSpace(content)
	for {
		start := strings.Index(content, "<@")
		if start < 0 {
			break
		}
		end := strings.Index(content[start:], ">")
		if end < 0 {
			break
		}
		content = content[:start] + content[start+end+1:]
	}
	return strings.TrimSpace(content)
}

func (b *Bot) reply(ctx context.Context, target replyTarget, messageID string, seq int, text string) error {
	token, err := b.accessToken(ctx)
	if err != nil {
		return err
	}
	path := ""
	switch target.Kind {
	case targetC2C:
		path = "/v2/users/" + target.OpenID + "/messages"
	case targetGroup:
		path = "/v2/groups/" + target.GroupOpenID + "/messages"
	default:
		return fmt.Errorf("unknown qqbot reply target: %s", target.Kind)
	}
	if seq <= 0 {
		seq = 1
	}
	body := map[string]any{
		"content":  strings.TrimSpace(text),
		"msg_type": 0,
		"msg_id":   messageID,
		"msg_seq":  seq,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "QQBot "+token)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qqbot reply failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	var decoded struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &decoded); err == nil && decoded.Code != 0 {
		return fmt.Errorf("qqbot reply failed: code=%d message=%s", decoded.Code, decoded.Message)
	}
	return nil
}

func (b *Bot) accessToken(ctx context.Context) (string, error) {
	now := time.Now()
	b.tokenMu.Lock()
	if b.token != "" && now.Before(b.tokenExpiry.Add(-60*time.Second)) {
		token := b.token
		b.tokenMu.Unlock()
		return token, nil
	}
	b.tokenMu.Unlock()

	data, err := json.Marshal(map[string]string{
		"appId":        b.appID,
		"clientSecret": b.secret,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://bots.qq.com/app/getAppAccessToken", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(b.baseURL, "http://127.0.0.1") || strings.HasPrefix(b.baseURL, "http://localhost") || strings.HasPrefix(b.baseURL, "http://[::1]") {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/app/getAppAccessToken", bytes.NewReader(data))
		if err != nil {
			return "", err
		}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("qqbot token failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}
	var decoded tokenResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", err
	}
	if decoded.AccessToken == "" {
		return "", fmt.Errorf("qqbot token response missing access_token: %s", string(respBody))
	}
	expiresIn := decoded.ExpiresInSeconds()
	if expiresIn <= 0 {
		expiresIn = 7200
	}

	b.tokenMu.Lock()
	b.token = decoded.AccessToken
	b.tokenExpiry = time.Now().Add(time.Duration(expiresIn) * time.Second)
	b.tokenMu.Unlock()
	return decoded.AccessToken, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func bodyString(body []byte) string {
	return string(body)
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type callbackPayload struct {
	ID   string          `json:"id"`
	Op   int             `json:"op"`
	Seq  int             `json:"s"`
	Type string          `json:"t"`
	Data json.RawMessage `json:"d"`
}

type validationPayload struct {
	PlainToken string     `json:"plain_token"`
	EventTS    jsonString `json:"event_ts"`
}

type jsonString string

func (s *jsonString) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = jsonString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*s = jsonString(number.String())
		return nil
	}
	return fmt.Errorf("expected string or number")
}

func (s jsonString) String() string {
	return string(s)
}

type callbackMessage struct {
	ID          string         `json:"id"`
	OpenID      string         `json:"openid"`
	GroupOpenID string         `json:"group_openid"`
	Content     string         `json:"content"`
	Author      callbackAuthor `json:"author"`
}

type callbackAuthor struct {
	OpenID       string `json:"openid"`
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
}

type replyTarget struct {
	Kind        string
	OpenID      string
	GroupOpenID string
}

type tokenResponse struct {
	AccessToken string          `json:"access_token"`
	ExpiresIn   json.RawMessage `json:"expires_in"`
}

func (r tokenResponse) ExpiresInSeconds() int {
	var number int
	if err := json.Unmarshal(r.ExpiresIn, &number); err == nil {
		return number
	}
	var text string
	if err := json.Unmarshal(r.ExpiresIn, &text); err == nil {
		parsed, _ := strconv.Atoi(text)
		return parsed
	}
	return 0
}
