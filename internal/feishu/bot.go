package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"

	"aics/internal/imagehost"
	"aics/internal/orchestrator"
)

type Bot struct {
	client             *lark.Client
	wsClient           *ws.Client
	dispatcher         *dispatcher.EventDispatcher
	orchestrator       *orchestrator.Orchestrator
	logger             *slog.Logger
	reactionReceived   string
	reactionProcessing string
	reactionDone       string
	reactionStates     *reactionStateStore
	logRawMessage      bool
	replyFormat        string
	stream             bool
	streamPlaceholder  string
	updateEvery        time.Duration
	deduper            Deduper
	imageInputMode     string
	imageUploader      *imagehost.Client
}

func NewBot(appID, appSecret, verificationToken, encryptKey, reactionReceived string, reactionProcessing string, reactionDone string, logRawMessage bool, replyFormat string, stream bool, streamPlaceholder string, updateEvery time.Duration, deduper Deduper, imageInputMode string, imageUploader *imagehost.Client, orch *orchestrator.Orchestrator, logger *slog.Logger) *Bot {
	if deduper == nil {
		deduper = NewMemoryDeduper(time.Hour)
	}
	bot := &Bot{
		client:             lark.NewClient(appID, appSecret),
		orchestrator:       orch,
		logger:             logger,
		reactionReceived:   reactionReceived,
		reactionProcessing: reactionProcessing,
		reactionDone:       reactionDone,
		reactionStates:     newReactionStateStore(),
		logRawMessage:      logRawMessage,
		replyFormat:        strings.ToLower(strings.TrimSpace(replyFormat)),
		stream:             stream,
		streamPlaceholder:  streamPlaceholder,
		updateEvery:        updateEvery,
		deduper:            deduper,
		imageInputMode:     strings.ToLower(strings.TrimSpace(imageInputMode)),
		imageUploader:      imageUploader,
	}

	eventDispatcher := dispatcher.NewEventDispatcher(verificationToken, encryptKey).
		OnP2MessageReceiveV1(bot.onMessage).
		OnP2MessageReactionCreatedV1(func(context.Context, *larkim.P2MessageReactionCreatedV1) error {
			return nil
		}).
		OnP2MessageReactionDeletedV1(func(context.Context, *larkim.P2MessageReactionDeletedV1) error {
			return nil
		})
	bot.dispatcher = eventDispatcher

	bot.wsClient = ws.NewClient(
		appID,
		appSecret,
		ws.WithEventHandler(eventDispatcher),
		ws.WithLogLevel(larkcore.LogLevelInfo),
	)

	return bot
}

func (b *Bot) Start(ctx context.Context) error {
	b.logger.Info("starting feishu websocket client")
	return b.wsClient.Start(ctx)
}

func (b *Bot) WebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if handled := writeChallengeResponse(w, body); handled {
			return
		}
		resp := b.dispatcher.Handle(r.Context(), &larkevent.EventReq{
			Header:     r.Header,
			Body:       body,
			RequestURI: r.RequestURI,
		})
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(resp.Body)
	}
}

func writeChallengeResponse(w http.ResponseWriter, body []byte) bool {
	var payload struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if strings.TrimSpace(payload.Challenge) == "" {
		return false
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"challenge": payload.Challenge,
	})
	return true
}

func (b *Bot) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	if b.logRawMessage {
		b.logRawIncoming(event)
	}
	incoming, ok := parseIncoming(event)
	if !ok {
		return nil
	}
	firstSeen, err := b.deduper.Mark(ctx, incoming.MessageID)
	if err != nil {
		b.logger.Warn("message dedupe failed; continuing", "message_id", incoming.MessageID, "err", err)
	} else if !firstSeen {
		b.logger.Info("duplicate message ignored", "message_id", incoming.MessageID)
		return nil
	}

	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := b.processIncoming(bgCtx, incoming); err != nil {
			b.logger.Error("process incoming message failed", "message_id", incoming.MessageID, "err", err)
		}
	}()
	return nil
}

func (b *Bot) processIncoming(ctx context.Context, incoming orchestrator.IncomingMessage) error {
	if err := b.setReactionState(ctx, incoming.MessageID, b.reactionReceived); err != nil {
		b.logger.Warn("received reaction failed", "message_id", incoming.MessageID, "emoji", b.reactionReceived, "err", err)
	}
	if err := b.enrichImages(ctx, &incoming); err != nil {
		b.logger.Warn("image upload failed", "message_id", incoming.MessageID, "err", err)
	}
	if err := b.setReactionState(ctx, incoming.MessageID, b.reactionProcessing); err != nil {
		b.logger.Warn("processing reaction failed", "message_id", incoming.MessageID, "emoji", b.reactionProcessing, "err", err)
	}

	if b.stream {
		if err := b.handleStream(ctx, incoming); err != nil {
			return err
		}
		return b.setReactionDone(ctx, incoming.MessageID)
	}

	reply, err := b.orchestrator.Handle(ctx, incoming)
	if err != nil {
		b.logger.Error("handle message failed", "message_id", incoming.MessageID, "err", err)
		reply = "抱歉，我处理这条消息时遇到了问题，请稍后再试或转人工。"
	}

	if _, err := b.reply(ctx, incoming.MessageID, reply); err != nil {
		b.logger.Error("reply message failed", "message_id", incoming.MessageID, "err", err)
		return err
	}
	return b.setReactionDone(ctx, incoming.MessageID)
}

func (b *Bot) logRawIncoming(event *larkim.P2MessageReceiveV1) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		b.logger.Info("raw feishu message event is empty")
		return
	}
	msg := event.Event.Message
	b.logger.Info(
		"raw feishu message received",
		"message_id", deref(msg.MessageId),
		"root_id", deref(msg.RootId),
		"parent_id", deref(msg.ParentId),
		"thread_id", deref(msg.ThreadId),
		"chat_id", deref(msg.ChatId),
		"chat_type", deref(msg.ChatType),
		"message_type", deref(msg.MessageType),
		"content", deref(msg.Content),
		"image_keys", strings.Join(extractImageKeys(deref(msg.Content)), ","),
		"image_urls", strings.Join(extractImageURLs(deref(msg.Content)), ","),
		"text", extractText(deref(msg.Content), deref(msg.MessageType)),
	)
}

func (b *Bot) enrichImages(ctx context.Context, incoming *orchestrator.IncomingMessage) error {
	if len(incoming.ImageURLs) > 0 || len(incoming.ImageKeys) == 0 {
		return nil
	}
	if b.imageInputMode != "base64" && (b.imageUploader == nil || !b.imageUploader.Enabled()) {
		return fmt.Errorf("image host uploader is not configured")
	}

	var uploaded []string
	for _, imageKey := range incoming.ImageKeys {
		b.logger.Info("downloading feishu image resource", "message_id", incoming.MessageID, "image_key", imageKey)
		data, fileName, mimeType, err := b.downloadImage(ctx, incoming.MessageID, imageKey)
		if err != nil {
			return err
		}
		if b.imageInputMode == "base64" {
			dataURL := imageDataURL(mimeType, data)
			b.logger.Info("converted feishu image to base64 data url", "message_id", incoming.MessageID, "image_key", imageKey, "file_name", fileName, "mime_type", mimeType, "bytes", len(data))
			uploaded = append(uploaded, dataURL)
		} else {
			b.logger.Info("uploading image to image host", "message_id", incoming.MessageID, "image_key", imageKey, "file_name", fileName, "bytes", len(data))
			url, err := b.imageUploader.Upload(ctx, fileName, data)
			if err != nil {
				return err
			}
			b.logger.Info("image uploaded", "message_id", incoming.MessageID, "image_key", imageKey, "url", url)
			uploaded = append(uploaded, url)
		}
	}
	incoming.ImageURLs = uploaded

	var bld strings.Builder
	if strings.TrimSpace(incoming.Text) != "" && !looksLikeImageOnlyJSON(incoming.Text) {
		bld.WriteString(strings.TrimSpace(incoming.Text))
		bld.WriteString("\n\n")
	}
	bld.WriteString("用户发送了图片，请结合图片内容回答。")
	for _, url := range uploaded {
		bld.WriteString("\n图片URL: ")
		if strings.HasPrefix(url, "data:") {
			bld.WriteString("[base64 image data]")
		} else {
			bld.WriteString(url)
		}
	}
	incoming.Text = bld.String()
	return nil
}

func looksLikeImageOnlyJSON(text string) bool {
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return false
	}
	_, hasImageKey := payload["image_key"].(string)
	return hasImageKey && len(payload) == 1
}

func (b *Bot) downloadImage(ctx context.Context, messageID string, imageKey string) ([]byte, string, string, error) {
	resp, err := b.client.Im.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build())
	if err != nil {
		return nil, "", "", err
	}
	if !resp.Success() {
		return nil, "", "", fmt.Errorf("feishu image resource download failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.File == nil {
		return nil, "", "", fmt.Errorf("feishu image resource response has no file")
	}
	data, err := io.ReadAll(resp.File)
	if err != nil {
		return nil, "", "", err
	}
	fileName := resp.FileName
	if strings.TrimSpace(fileName) == "" {
		fileName = imageKey + ".jpg"
	}
	fileName = filepath.Base(fileName)
	mimeType := http.DetectContentType(data)
	return data, fileName, mimeType, nil
}

func imageDataURL(mimeType string, data []byte) string {
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/jpeg"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func (b *Bot) handleStream(ctx context.Context, incoming orchestrator.IncomingMessage) error {
	replyMessageID := ""
	if strings.TrimSpace(b.streamPlaceholder) != "" {
		var err error
		replyMessageID, err = b.reply(ctx, incoming.MessageID, b.streamPlaceholder)
		if err != nil {
			b.logger.Warn("placeholder reply failed; stream updates disabled for this message", "message_id", incoming.MessageID, "err", err)
			replyMessageID = ""
		}
		if replyMessageID == "" {
			b.logger.Warn("placeholder reply returned empty message id; stream updates will fall back to final reply", "source_message_id", incoming.MessageID)
		}
	}

	lastUpdate := time.Time{}
	latestContent := ""
	deliveredContent := ""
	update := func(content string, done bool) error {
		content = strings.TrimSpace(content)
		if content == "" {
			return nil
		}
		if !done && content == latestContent {
			return nil
		}
		latestContent = content
		if replyMessageID == "" {
			return nil
		}
		if !done && !lastUpdate.IsZero() && time.Since(lastUpdate) < b.updateEvery {
			return nil
		}
		lastUpdate = time.Now()
		if err := b.updateMessage(ctx, replyMessageID, content); err != nil {
			b.logger.Warn("stream update failed", "reply_message_id", replyMessageID, "err", err)
			return nil
		}
		deliveredContent = content
		return nil
	}

	reply, err := b.orchestrator.HandleStream(ctx, incoming, update)
	if err != nil {
		b.logger.Error("handle stream message failed", "message_id", incoming.MessageID, "err", err)
		return b.finishStream(ctx, incoming.MessageID, replyMessageID, deliveredContent, "抱歉，我处理这条消息时遇到了问题，请稍后再试或转人工。")
	}
	if strings.TrimSpace(reply) != "" && reply != deliveredContent {
		return b.finishStream(ctx, incoming.MessageID, replyMessageID, deliveredContent, reply)
	}
	return nil
}

func (b *Bot) finishStream(ctx context.Context, sourceMessageID string, replyMessageID string, deliveredContent string, finalText string) error {
	if replyMessageID != "" {
		if err := b.updateMessage(ctx, replyMessageID, finalText); err == nil {
			return nil
		} else {
			b.logger.Warn("final stream update failed; sending final reply as fallback", "reply_message_id", replyMessageID, "err", err)
		}
	}
	if strings.TrimSpace(deliveredContent) == strings.TrimSpace(finalText) {
		return nil
	}
	if _, err := b.reply(ctx, sourceMessageID, finalText); err != nil {
		b.logger.Error("fallback final reply failed", "source_message_id", sourceMessageID, "err", err)
		return err
	}
	return nil
}

func (b *Bot) setReactionDone(ctx context.Context, messageID string) error {
	if err := b.setReactionState(ctx, messageID, b.reactionDone); err != nil {
		b.logger.Warn("done reaction failed", "message_id", messageID, "emoji", b.reactionDone, "err", err)
	}
	return nil
}

func (b *Bot) setReactionState(ctx context.Context, messageID string, emoji string) error {
	emoji = strings.TrimSpace(emoji)
	if emoji == "" {
		return nil
	}
	if previous := b.reactionStates.Get(messageID); previous != "" {
		if err := b.deleteReaction(ctx, messageID, previous); err != nil {
			b.logger.Warn("delete previous reaction failed", "message_id", messageID, "reaction_id", previous, "err", err)
		}
	}
	reactionID, err := b.react(ctx, messageID, emoji)
	if err != nil {
		return err
	}
	b.reactionStates.Set(messageID, reactionID)
	return nil
}

func (b *Bot) react(ctx context.Context, messageID string, emoji string) (string, error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emoji).Build()).
			Build()).
		Build()

	resp, err := b.client.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return "", nil
	}
	return deref(resp.Data.ReactionId), nil
}

func (b *Bot) deleteReaction(ctx context.Context, messageID string, reactionID string) error {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	resp, err := b.client.Im.MessageReaction.Delete(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu delete reaction failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	b.reactionStates.Set(messageID, "")
	return nil
}

func (b *Bot) reply(ctx context.Context, messageID string, text string) (string, error) {
	msgType, content, err := b.replyPayload(text)
	if err != nil {
		return "", err
	}
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			ReplyInThread(true).
			Uuid(uuid.NewString()).
			Build()).
		Build()

	resp, err := b.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu reply failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil {
		return "", nil
	}
	return deref(resp.Data.MessageId), nil
}

func (b *Bot) updateMessage(ctx context.Context, messageID string, text string) error {
	if messageID == "" {
		return fmt.Errorf("message id is empty")
	}
	msgType, content, err := b.replyPayload(text)
	if err != nil {
		return err
	}
	if msgType == larkim.MsgTypeInteractive {
		req := larkim.NewPatchMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewPatchMessageReqBodyBuilder().
				Content(content).
				Build()).
			Build()

		resp, err := b.client.Im.Message.Patch(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu card patch failed: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	req := larkim.NewUpdateMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewUpdateMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			Build()).
		Build()

	resp, err := b.client.Im.Message.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu update failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (b *Bot) replyPayload(text string) (string, string, error) {
	switch b.replyFormat {
	case "markdown_card", "card", "interactive":
		content, err := markdownCardContent(text)
		return larkim.MsgTypeInteractive, content, err
	case "text", "":
		return larkim.MsgTypeText, larkim.NewTextMsgBuilder().Text(escapeText(text)).Build(), nil
	default:
		return larkim.MsgTypeText, larkim.NewTextMsgBuilder().Text(escapeText(text)).Build(), nil
	}
}

func markdownCardContent(markdown string) (string, error) {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
		},
		"body": map[string]any{
			"direction": "vertical",
			"padding":   "12px 12px 12px 12px",
			"elements": []map[string]any{
				{
					"tag":        "markdown",
					"element_id": "answer",
					"content":    markdown,
					"text_align": "left",
					"text_size":  "large",
				},
			},
		},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func parseIncoming(event *larkim.P2MessageReceiveV1) (orchestrator.IncomingMessage, bool) {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		return orchestrator.IncomingMessage{}, false
	}

	msg := event.Event.Message
	messageID := deref(msg.MessageId)
	chatID := deref(msg.ChatId)
	threadID := deref(msg.ThreadId)
	rootID := deref(msg.RootId)
	if threadID == "" {
		if rootID != "" {
			threadID = rootID
		} else {
			threadID = messageID
			rootID = messageID
		}
	}
	if rootID == "" {
		rootID = threadID
	}

	senderID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil {
		senderID = firstNonEmpty(
			deref(event.Event.Sender.SenderId.OpenId),
			deref(event.Event.Sender.SenderId.UserId),
			deref(event.Event.Sender.SenderId.UnionId),
		)
	}

	text := extractText(deref(msg.Content), deref(msg.MessageType))
	text = stripMentionTags(text)
	imageKeys := extractImageKeys(deref(msg.Content))
	imageURLs := extractImageURLs(deref(msg.Content))

	return orchestrator.IncomingMessage{
		MessageID:     messageID,
		ChatID:        chatID,
		ThreadID:      threadID,
		RootMessageID: rootID,
		SenderID:      senderID,
		Text:          strings.TrimSpace(text),
		ImageKeys:     imageKeys,
		ImageURLs:     imageURLs,
	}, messageID != "" && chatID != "" && threadID != ""
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
		return text
	}
	if extracted := strings.TrimSpace(collectText(payload)); extracted != "" {
		return extracted
	}
	return content
}

func collectText(value any) string {
	var parts []string
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			for _, key := range []string{"title", "text", "name"} {
				if value, ok := typed[key].(string); ok && strings.TrimSpace(value) != "" {
					parts = append(parts, strings.TrimSpace(value))
				}
			}
			for key, value := range typed {
				switch key {
				case "title", "text", "name", "user_id", "open_id", "union_id", "image_key":
					continue
				default:
					walk(value)
				}
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		case string:
			if strings.TrimSpace(typed) != "" {
				parts = append(parts, strings.TrimSpace(typed))
			}
		}
	}
	walk(value)
	return strings.Join(uniqueText(parts), "\n")
}

func extractImageKeys(content string) []string {
	if content == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	var keys []string
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			if value, ok := typed["image_key"].(string); ok && strings.TrimSpace(value) != "" {
				keys = append(keys, strings.TrimSpace(value))
			}
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(payload)
	return uniqueText(keys)
}

func extractImageURLs(content string) []string {
	if content == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil
	}
	var urls []string
	var walk func(any)
	walk = func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			for _, key := range []string{"url", "image_url", "src"} {
				if value, ok := typed[key].(string); ok && isHTTPURL(value) {
					urls = append(urls, strings.TrimSpace(value))
				}
			}
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		case string:
			if isHTTPURL(typed) {
				urls = append(urls, strings.TrimSpace(typed))
			}
		}
	}
	walk(payload)
	return uniqueText(urls)
}

func isHTTPURL(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func uniqueText(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stripMentionTags(text string) string {
	for {
		start := strings.Index(text, "<at ")
		if start < 0 {
			return text
		}
		end := strings.Index(text[start:], "</at>")
		if end < 0 {
			return text
		}
		text = text[:start] + text[start+end+len("</at>"):]
	}
}

func escapeText(text string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(text)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
