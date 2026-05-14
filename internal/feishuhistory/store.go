package feishuhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"aics/internal/session"
)

type Store struct {
	client         *lark.Client
	lookback       time.Duration
	maxFetchFactor int
}

func NewStore(appID, appSecret string, lookback time.Duration) *Store {
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	return &Store{
		client:         lark.NewClient(appID, appSecret),
		lookback:       lookback,
		maxFetchFactor: 4,
	}
}

func (s *Store) GetOrCreate(_ context.Context, key session.SessionKey) (*session.Session, error) {
	now := time.Now()
	id := key.ChatID + ":" + key.ThreadID
	return &session.Session{
		ID:            id,
		ChatID:        key.ChatID,
		ThreadID:      key.ThreadID,
		RootMessageID: key.RootMessageID,
		CreatedBy:     key.CreatedBy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (s *Store) AddMessage(_ context.Context, _ session.Message) error {
	return nil
}

func (s *Store) RecentMessages(ctx context.Context, sessionID string, limit int) ([]session.Message, error) {
	_, threadID, ok := strings.Cut(sessionID, ":")
	if !ok || strings.TrimSpace(threadID) == "" {
		return nil, fmt.Errorf("invalid feishu history session id: %s", sessionID)
	}
	if limit <= 0 {
		limit = 20
	}
	pageSize := limit * s.maxFetchFactor
	if pageSize < 20 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	resp, err := s.client.Im.Message.List(ctx, larkim.NewListMessageReqBuilder().
		ContainerIdType("thread").
		ContainerId(threadID).
		SortType(larkim.SortTypeListMessageByCreateTimeDesc).
		PageSize(pageSize).
		Build())
	if err != nil {
		return nil, err
	}
	if !resp.Success() {
		return nil, fmt.Errorf("list feishu messages failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return nil, nil
	}

	var history []session.Message
	for _, item := range resp.Data.Items {
		msg, ok := toSessionMessage(item, sessionID)
		if !ok {
			continue
		}
		history = append(history, msg)
	}
	sort.Slice(history, func(i, j int) bool {
		return history[i].CreatedAt.Before(history[j].CreatedAt)
	})
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	return history, nil
}

func toSessionMessage(item *larkim.Message, sessionID string) (session.Message, bool) {
	if item == nil || item.Body == nil || item.Body.Content == nil {
		return session.Message{}, false
	}
	if item.Deleted != nil && *item.Deleted {
		return session.Message{}, false
	}

	role := session.RoleUser
	if item.Sender != nil && strings.EqualFold(deref(item.Sender.SenderType), "app") {
		role = session.RoleAssistant
	}
	content := strings.TrimSpace(extractText(deref(item.Body.Content), deref(item.MsgType)))
	if content == "" || looksLikeReactionOnly(content) {
		return session.Message{}, false
	}
	return session.Message{
		ID:              deref(item.MessageId),
		SessionID:       sessionID,
		FeishuMessageID: deref(item.MessageId),
		Role:            role,
		Content:         content,
		CreatedAt:       parseMillis(deref(item.CreateTime)),
	}, true
}

func extractText(content string, messageType string) string {
	if content == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return content
	}
	if text, ok := payload["text"].(string); ok && strings.TrimSpace(text) != "" {
		return stripMentionTags(text)
	}
	if extracted := strings.TrimSpace(collectText(payload)); extracted != "" {
		return stripMentionTags(extracted)
	}
	return content
}

func collectText(value any) string {
	var parts []string
	var walk func(any)
	walk = func(v any) {
		switch item := v.(type) {
		case map[string]any:
			for key, child := range item {
				if key == "text" || key == "content" {
					if text, ok := child.(string); ok {
						parts = append(parts, text)
						continue
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(value)
	return strings.Join(parts, "")
}

func stripMentionTags(text string) string {
	for {
		start := strings.Index(text, "<at ")
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], "</at>")
		if end < 0 {
			break
		}
		text = text[:start] + text[start+end+len("</at>"):]
	}
	return strings.TrimSpace(text)
}

func looksLikeReactionOnly(text string) bool {
	text = strings.TrimSpace(text)
	return text == "" || text == "正在处理..."
}

func parseMillis(value string) time.Time {
	millis, err := parseInt64(value)
	if err != nil || millis <= 0 {
		return time.Now()
	}
	return time.UnixMilli(millis)
}

func parseInt64(value string) (int64, error) {
	var out int64
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer: %s", value)
		}
		out = out*10 + int64(r-'0')
	}
	return out, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
