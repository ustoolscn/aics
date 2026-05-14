package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"aics/internal/llm"
	"aics/internal/prompt"
	"aics/internal/session"
	"aics/internal/tool"
)

type IncomingMessage struct {
	MessageID     string
	ChatID        string
	ThreadID      string
	RootMessageID string
	SenderID      string
	Text          string
	ImageKeys     []string
	ImageURLs     []string
}

type Orchestrator struct {
	store      session.Store
	prompts    *prompt.Loader
	llm        llm.Client
	tools      *tool.Registry
	maxHistory int
	skillIndex string
}

func New(store session.Store, prompts *prompt.Loader, llmClient llm.Client, tools *tool.Registry, maxHistory int) *Orchestrator {
	return &Orchestrator{
		store:      store,
		prompts:    prompts,
		llm:        llmClient,
		tools:      tools,
		maxHistory: maxHistory,
	}
}

func (o *Orchestrator) SetSkillIndexPrompt(prompt string) {
	o.skillIndex = strings.TrimSpace(prompt)
}

type StreamUpdate func(content string, done bool) error

func (o *Orchestrator) Handle(ctx context.Context, incoming IncomingMessage) (string, error) {
	return o.handle(ctx, incoming, nil)
}

func (o *Orchestrator) HandleStream(ctx context.Context, incoming IncomingMessage, onUpdate StreamUpdate) (string, error) {
	return o.handle(ctx, incoming, onUpdate)
}

func (o *Orchestrator) handle(ctx context.Context, incoming IncomingMessage, onUpdate StreamUpdate) (string, error) {
	if strings.TrimSpace(incoming.Text) == "" {
		reply := "我收到了这条消息，但目前只能处理文本内容。"
		if onUpdate != nil {
			_ = onUpdate(reply, true)
		}
		return reply, nil
	}

	sess, err := o.store.GetOrCreate(ctx, session.SessionKey{
		ChatID:        incoming.ChatID,
		ThreadID:      incoming.ThreadID,
		RootMessageID: incoming.RootMessageID,
		CreatedBy:     incoming.SenderID,
	})
	if err != nil {
		return "", err
	}

	userMsg := session.Message{
		ID:              uuid.NewString(),
		SessionID:       sess.ID,
		FeishuMessageID: incoming.MessageID,
		Role:            session.RoleUser,
		Content:         incoming.Text,
		ImageURLs:       incoming.ImageURLs,
		CreatedAt:       time.Now(),
	}
	if err := o.store.AddMessage(ctx, userMsg); err != nil {
		return "", err
	}

	systemPrompt, err := o.prompts.Load()
	if err != nil {
		return "", err
	}

	history, err := o.store.RecentMessages(ctx, sess.ID, o.maxHistory)
	if err != nil {
		return "", err
	}
	history = ensureCurrentUserMessage(history, userMsg, o.maxHistory)

	messages := buildLLMMessages(systemPrompt, o.skillIndex, sess, history)
	if len(incoming.ImageURLs) > 0 {
		final, err := o.finalAnswer(ctx, messages, onUpdate)
		if err != nil {
			return "", err
		}
		return o.saveAndReturnReply(ctx, sess.ID, final)
	}

	first, err := o.llm.Chat(ctx, llm.ChatRequest{
		Messages: messages,
		Tools:    o.tools.Definitions(),
	})
	if err != nil {
		return "", err
	}

	final := first.Message
	if len(first.Message.ToolCalls) > 0 {
		messages = append(messages, first.Message)
		for _, call := range first.Message.ToolCalls {
			result, found, err := o.tools.Call(ctx, call.Function.Name, json.RawMessage(call.Function.Arguments))
			if err != nil {
				result = fmt.Sprintf("工具 %s 调用失败：%v", call.Function.Name, err)
			}
			if !found {
				result = fmt.Sprintf("工具 %s 不存在。", call.Function.Name)
			}
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: call.ID,
				Content:    result,
			})
		}
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: "请基于上面的工具结果，直接生成给用户的最终回复。不要继续调用工具；如果工具返回了错误，请把错误用用户能理解的话说明，并提示可以如何补充信息。",
		})

		final, err = o.finalAnswer(ctx, messages, onUpdate)
		if err != nil {
			return "", err
		}
	} else if onUpdate != nil {
		streamText(llm.MessageContentString(first.Message), onUpdate)
	}

	return o.saveAndReturnReply(ctx, sess.ID, final)
}

func ensureCurrentUserMessage(history []session.Message, current session.Message, limit int) []session.Message {
	for _, item := range history {
		if current.FeishuMessageID != "" && item.FeishuMessageID == current.FeishuMessageID {
			return history
		}
	}
	history = append(history, current)
	if limit > 0 && len(history) > limit {
		history = history[len(history)-limit:]
	}
	return history
}

func (o *Orchestrator) saveAndReturnReply(ctx context.Context, sessionID string, final llm.Message) (string, error) {
	reply := strings.TrimSpace(llm.MessageContentString(final))
	if reply == "" {
		reply = "我暂时没有生成有效回复，可以请你补充一下问题细节吗？"
	}

	if err := o.store.AddMessage(ctx, session.Message{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Role:      session.RoleAssistant,
		Content:   reply,
		CreatedAt: time.Now(),
	}); err != nil {
		return "", err
	}

	return reply, nil
}

func (o *Orchestrator) finalAnswer(ctx context.Context, messages []llm.Message, onUpdate StreamUpdate) (llm.Message, error) {
	if onUpdate != nil {
		if streamClient, ok := o.llm.(llm.StreamClient); ok {
			var full strings.Builder
			resp, err := streamClient.ChatStream(ctx, llm.ChatRequest{Messages: messages}, func(delta string) error {
				full.WriteString(delta)
				return onUpdate(full.String(), false)
			})
			if err != nil {
				return llm.Message{}, err
			}
			if llm.MessageContentString(resp.Message) == "" {
				resp.Message.Content = full.String()
			}
			if strings.TrimSpace(llm.MessageContentString(resp.Message)) != "" {
				_ = onUpdate(llm.MessageContentString(resp.Message), true)
				return resp.Message, nil
			}
		}
	}

	resp, err := o.llm.Chat(ctx, llm.ChatRequest{Messages: messages})
	if err != nil {
		return llm.Message{}, err
	}
	return resp.Message, nil
}

func streamText(text string, onUpdate StreamUpdate) {
	runes := []rune(text)
	if len(runes) == 0 {
		_ = onUpdate("", true)
		return
	}
	step := 12
	for i := step; i < len(runes); i += step {
		_ = onUpdate(string(runes[:i]), false)
	}
	_ = onUpdate(text, true)
}

func buildLLMMessages(systemPrompt string, skillIndex string, sess *session.Session, history []session.Message) []llm.Message {
	if strings.TrimSpace(skillIndex) != "" {
		systemPrompt += "\n\n" + strings.TrimSpace(skillIndex)
	}
	messages := []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: systemPrompt + "\n\n当前飞书话题信息：\n" +
				"- session_id: " + sess.ID + "\n" +
				"- chat_id: " + sess.ChatID + "\n" +
				"- thread_id: " + sess.ThreadID + "\n",
		},
	}
	if sess.Summary != "" {
		messages = append(messages, llm.Message{
			Role:    llm.RoleSystem,
			Content: "当前话题摘要：\n" + sess.Summary,
		})
	}
	for _, msg := range history {
		switch msg.Role {
		case session.RoleUser:
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: userContent(msg.Content, msg.ImageURLs)})
		case session.RoleAssistant:
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: msg.Content})
		case session.RoleTool:
			messages = append(messages, llm.Message{Role: llm.RoleTool, Content: msg.Content})
		}
	}
	return messages
}

func userContent(text string, imageURLs []string) any {
	if len(imageURLs) == 0 {
		return text
	}
	parts := []llm.ContentPart{
		{Type: "text", Text: text},
	}
	for _, url := range imageURLs {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type:     "image_url",
			ImageURL: &llm.ImageURL{URL: url},
		})
	}
	return parts
}
