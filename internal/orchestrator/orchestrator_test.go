package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aics/internal/llm"
	"aics/internal/prompt"
	"aics/internal/session"
	"aics/internal/tool"
)

func TestHandleKeepsThreadSessionsIsolated(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "customer_service.md")
	if err := os.WriteFile(promptFile, []byte("你是客服。"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &recordingLLM{
		responses: []llm.Message{
			{Role: llm.RoleAssistant, Content: "A reply"},
			{Role: llm.RoleAssistant, Content: "B reply"},
		},
	}
	orch := New(
		session.NewMemoryStore(),
		prompt.NewLoader(promptFile),
		fake,
		tool.NewRegistry(),
		20,
	)

	if _, err := orch.Handle(ctx, IncomingMessage{
		MessageID: "m1", ChatID: "chat", ThreadID: "thread-a", RootMessageID: "m1", SenderID: "u1", Text: "A 的问题",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Handle(ctx, IncomingMessage{
		MessageID: "m2", ChatID: "chat", ThreadID: "thread-b", RootMessageID: "m2", SenderID: "u2", Text: "B 的问题",
	}); err != nil {
		t.Fatal(err)
	}

	if len(fake.requests) != 2 {
		t.Fatalf("expected 2 llm calls, got %d", len(fake.requests))
	}
	secondPrompt := flatten(fake.requests[1].Messages)
	if strings.Contains(secondPrompt, "A 的问题") || strings.Contains(secondPrompt, "A reply") {
		t.Fatalf("thread B prompt leaked thread A context: %s", secondPrompt)
	}
	if !strings.Contains(secondPrompt, "B 的问题") {
		t.Fatalf("thread B prompt missing its own message: %s", secondPrompt)
	}
}

func TestHandleExecutesKnowledgeTool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "customer_service.md")
	kbDir := filepath.Join(dir, "knowledge")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptFile, []byte("你是客服，可以使用知识库。"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "faq.md"), []byte("退款争议需要转人工处理。"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &recordingLLM{
		responses: []llm.Message{
			{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "knowledge_search",
							Arguments: `{"query":"退款争议"}`,
						},
					},
				},
			},
			{Role: llm.RoleAssistant, Content: "退款争议建议转人工处理。"},
		},
	}

	orch := New(
		session.NewMemoryStore(),
		prompt.NewLoader(promptFile),
		fake,
		tool.NewRegistry(tool.NewKnowledgeSearch(kbDir)),
		20,
	)

	reply, err := orch.Handle(ctx, IncomingMessage{
		MessageID: "m1", ChatID: "chat", ThreadID: "thread-a", RootMessageID: "m1", SenderID: "u1", Text: "我有退款争议",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "退款争议建议转人工处理。" {
		t.Fatalf("unexpected reply: %s", reply)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("expected tool loop to call llm twice, got %d", len(fake.requests))
	}
	secondPrompt := flatten(fake.requests[1].Messages)
	if !strings.Contains(secondPrompt, "退款争议需要转人工处理") {
		t.Fatalf("tool result was not sent back to llm: %s", secondPrompt)
	}
}

func TestHandleRetriesEmptyAssistantMessage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "customer_service.md")
	if err := os.WriteFile(promptFile, []byte("You are support."), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &recordingLLM{
		responses: []llm.Message{
			{Role: llm.RoleAssistant},
			{Role: llm.RoleAssistant, Content: "final reply"},
		},
	}
	orch := New(
		session.NewMemoryStore(),
		prompt.NewLoader(promptFile),
		fake,
		tool.NewRegistry(),
		20,
	)

	reply, err := orch.Handle(ctx, IncomingMessage{
		MessageID: "m1", ChatID: "chat", ThreadID: "thread-a", RootMessageID: "m1", SenderID: "u1", Text: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "final reply" {
		t.Fatalf("unexpected reply: %s", reply)
	}
	if len(fake.requests) != 2 {
		t.Fatalf("expected retry after empty assistant message, got %d calls", len(fake.requests))
	}
}

func TestHandleStreamsInitialToolDecision(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "customer_service.md")
	kbDir := filepath.Join(dir, "knowledge")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptFile, []byte("You are support."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "faq.md"), []byte("refund needs support"), 0o600); err != nil {
		t.Fatal(err)
	}

	fake := &streamingRecordingLLM{
		streamResponses: []llm.Message{
			{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: llm.FunctionCall{
							Name:      "knowledge_search",
							Arguments: `{"query":"refund"}`,
						},
					},
				},
			},
			{Role: llm.RoleAssistant, Content: "use support"},
		},
	}
	orch := New(
		session.NewMemoryStore(),
		prompt.NewLoader(promptFile),
		fake,
		tool.NewRegistry(tool.NewKnowledgeSearch(kbDir)),
		20,
	)

	var updates []string
	reply, err := orch.HandleStream(ctx, IncomingMessage{
		MessageID: "m1", ChatID: "chat", ThreadID: "thread-a", RootMessageID: "m1", SenderID: "u1", Text: "refund",
	}, func(content string, _ bool) error {
		updates = append(updates, content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "use support" {
		t.Fatalf("unexpected reply: %s", reply)
	}
	if fake.streamCalls != 2 {
		t.Fatalf("expected two stream calls, got %d", fake.streamCalls)
	}
	if len(fake.chatRequests) != 0 {
		t.Fatalf("expected no non-stream calls, got %d", len(fake.chatRequests))
	}
	if len(updates) == 0 || updates[len(updates)-1] != "use support" {
		t.Fatalf("expected final stream update, got %#v", updates)
	}
}

type recordingLLM struct {
	requests  []llm.ChatRequest
	responses []llm.Message
}

func (r *recordingLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	r.requests = append(r.requests, req)
	if len(r.responses) == 0 {
		return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
	}
	next := r.responses[0]
	r.responses = r.responses[1:]
	return &llm.ChatResponse{Message: next}, nil
}

type streamingRecordingLLM struct {
	chatRequests    []llm.ChatRequest
	streamRequests  []llm.ChatRequest
	streamResponses []llm.Message
	streamCalls     int
}

func (r *streamingRecordingLLM) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	r.chatRequests = append(r.chatRequests, req)
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "chat fallback"}}, nil
}

func (r *streamingRecordingLLM) ChatStream(_ context.Context, req llm.ChatRequest, onDelta func(delta string) error) (*llm.ChatResponse, error) {
	r.streamCalls++
	r.streamRequests = append(r.streamRequests, req)
	if len(r.streamResponses) == 0 {
		if onDelta != nil {
			if err := onDelta("stream fallback"); err != nil {
				return nil, err
			}
		}
		return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "stream fallback"}}, nil
	}
	next := r.streamResponses[0]
	r.streamResponses = r.streamResponses[1:]
	if text := llm.MessageContentString(next); text != "" && onDelta != nil {
		if err := onDelta(text); err != nil {
			return nil, err
		}
	}
	return &llm.ChatResponse{Message: next}, nil
}

func flatten(messages []llm.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		b.WriteString(string(msg.Role))
		b.WriteString(":")
		b.WriteString(llm.ContentString(msg.Content))
		b.WriteString("\n")
	}
	return b.String()
}
