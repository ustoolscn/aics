package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamToolCallAccumulator(t *testing.T) {
	acc := newStreamToolCallAccumulator()

	acc.Add([]struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}{
		{
			Index: 0,
			ID:    "call-1",
			Type:  "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "knowledge_search", Arguments: `{"query":"hel`},
		},
		{
			Index: 0,
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Arguments: `lo"}`},
		},
	})

	got := acc.List()
	if len(got) != 1 {
		t.Fatalf("expected one tool call, got %#v", got)
	}
	if got[0].ID != "call-1" || got[0].Type != "function" {
		t.Fatalf("unexpected tool call metadata: %#v", got[0])
	}
	if got[0].Function.Name != "knowledge_search" {
		t.Fatalf("unexpected function name: %q", got[0].Function.Name)
	}
	if got[0].Function.Arguments != `{"query":"hello"}` {
		t.Fatalf("unexpected arguments: %q", got[0].Function.Arguments)
	}
}

func TestResponsesChatUsesResponsesEndpointAndHostedTools(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1",
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "查到了"},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL+"/v1", "test-key", "gpt-test", time.Second, false, []string{"web_search_preview"})
	resp, err := client.Chat(contextWithTimeout(t), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "你是客服。"},
			{Role: RoleUser, Content: "今天有什么好消息？"},
		},
		Tools: []Tool{
			{
				Type: "function",
				Function: ToolFunction{
					Name:        "knowledge_search",
					Description: "查询知识库",
					Parameters: map[string]any{
						"type":       "object",
						"properties": map[string]any{"query": map[string]any{"type": "string"}},
						"required":   []string{"query"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ContentString(resp.Message.Content) != "查到了" {
		t.Fatalf("unexpected response content: %#v", resp.Message.Content)
	}

	if captured["model"] != "gpt-test" {
		t.Fatalf("unexpected model: %#v", captured["model"])
	}
	input := captured["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected two input messages, got %#v", input)
	}
	first := input[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "你是客服。" {
		t.Fatalf("unexpected system input: %#v", first)
	}

	tools := captured["tools"].([]any)
	if !hasToolType(tools, "web_search_preview") {
		t.Fatalf("hosted web search tool missing: %#v", tools)
	}
	functionTool := findToolByName(tools, "knowledge_search")
	if functionTool == nil {
		t.Fatalf("function tool missing: %#v", tools)
	}
	if functionTool["type"] != "function" || functionTool["description"] != "查询知识库" {
		t.Fatalf("unexpected function tool payload: %#v", functionTool)
	}
	if _, ok := functionTool["function"]; ok {
		t.Fatalf("responses function tools must not use chat-completions function wrapper: %#v", functionTool)
	}
}

func TestResponsesChatParsesFunctionCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "resp_1",
			"output": []map[string]any{
				{
					"type":      "function_call",
					"id":        "fc_1",
					"call_id":   "call_1",
					"name":      "knowledge_search",
					"arguments": `{"query":"退款"}`,
				},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL+"/v1", "test-key", "gpt-test", time.Second, false, nil)
	resp, err := client.Chat(contextWithTimeout(t), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "退款怎么处理？"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected one function call, got %#v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Type != "function" || call.Function.Name != "knowledge_search" || call.Function.Arguments != `{"query":"退款"}` {
		t.Fatalf("unexpected tool call: %#v", call)
	}
}

func TestResponsesChatSendsFunctionCallOutputs(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": "退款争议建议转人工。"},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL+"/v1", "test-key", "gpt-test", time.Second, false, nil)
	_, err := client.Chat(contextWithTimeout(t), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "我有退款争议"},
			{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: FunctionCall{
							Name:      "knowledge_search",
							Arguments: `{"query":"退款争议"}`,
						},
					},
				},
			},
			{Role: RoleTool, ToolCallID: "call_1", Content: "退款争议需要转人工处理。"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	input := captured["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("expected three input items, got %#v", input)
	}
	functionCall := input[1].(map[string]any)
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "call_1" || functionCall["name"] != "knowledge_search" {
		t.Fatalf("unexpected function call input item: %#v", functionCall)
	}
	functionOutput := input[2].(map[string]any)
	if functionOutput["type"] != "function_call_output" || functionOutput["call_id"] != "call_1" || functionOutput["output"] != "退款争议需要转人工处理。" {
		t.Fatalf("unexpected function output input item: %#v", functionOutput)
	}
}

func TestResponsesStreamParsesTextDeltasAndFunctionCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured map[string]any
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if captured["stream"] != true {
			t.Fatalf("expected stream=true, got %#v", captured["stream"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"处理中"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"knowledge_search","arguments":"{\"query\":\"退款\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL+"/v1", "test-key", "gpt-test", time.Second, false, nil)
	var deltas []string
	resp, err := client.ChatStream(contextWithTimeout(t), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "退款"}},
	}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "处理中" {
		t.Fatalf("unexpected deltas: %#v", deltas)
	}
	if ContentString(resp.Message.Content) != "处理中" {
		t.Fatalf("unexpected streamed content: %#v", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected one streamed function call, got %#v", resp.Message.ToolCalls)
	}
	if resp.Message.ToolCalls[0].Function.Arguments != `{"query":"退款"}` {
		t.Fatalf("unexpected streamed arguments: %#v", resp.Message.ToolCalls[0])
	}
}

func TestResponsesStreamMergesFunctionArgumentDeltasWithDoneItem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"query\""}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":":\"退款\"}"}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"knowledge_search"}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(server.URL+"/v1", "test-key", "gpt-test", time.Second, false, nil)
	resp, err := client.ChatStream(contextWithTimeout(t), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "退款"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected one merged function call, got %#v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Function.Name != "knowledge_search" || call.Function.Arguments != `{"query":"退款"}` {
		t.Fatalf("unexpected merged function call: %#v", call)
	}
}

func contextWithTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func hasToolType(tools []any, toolType string) bool {
	for _, item := range tools {
		tool := item.(map[string]any)
		if tool["type"] == toolType {
			return true
		}
	}
	return false
}

func findToolByName(tools []any, name string) map[string]any {
	for _, item := range tools {
		tool := item.(map[string]any)
		if tool["name"] == name {
			return tool
		}
	}
	return nil
}
