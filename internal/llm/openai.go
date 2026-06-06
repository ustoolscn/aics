package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type OpenAIClient struct {
	baseURL     string
	apiKey      string
	model       string
	logRequest  bool
	hostedTools []string
	httpClient  *http.Client
}

func NewOpenAIClient(baseURL, apiKey, model string, timeout time.Duration, logRequest bool, hostedTools []string) *OpenAIClient {
	return &OpenAIClient{
		baseURL:     baseURL,
		apiKey:      apiKey,
		model:       model,
		logRequest:  logRequest,
		hostedTools: normalizeHostedTools(hostedTools),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *OpenAIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	payload := c.responsesPayload(req, false)
	c.logPayload("responses", payload)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm responses request failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var decoded responsesResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("llm responses request failed: %s", decoded.Error.Message)
	}

	message := responseToMessage(decoded)
	if strings.TrimSpace(MessageContentString(message)) == "" && len(message.ToolCalls) == 0 {
		slog.Warn("llm response has empty assistant content", "body", truncate(string(respBody), 2000))
	}
	return &ChatResponse{Message: message}, nil
}

func (c *OpenAIClient) ChatStream(ctx context.Context, req ChatRequest, onDelta func(delta string) error) (*ChatResponse, error) {
	payload := c.responsesPayload(req, true)
	c.logPayload("responses_stream", payload)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.responsesURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("llm responses stream request failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var full bytes.Buffer
	toolCalls := newResponsesStreamToolCallAccumulator()
	var completed *responsesResponse
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var event responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return nil, err
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta == "" {
				continue
			}
			full.WriteString(event.Delta)
			if onDelta != nil {
				if err := onDelta(event.Delta); err != nil {
					return nil, err
				}
			}
		case "response.function_call_arguments.delta":
			toolCalls.AddArgumentsDelta(event.OutputIndex, event.ItemID, event.Delta)
		case "response.function_call_arguments.done":
			toolCalls.SetArguments(event.OutputIndex, event.ItemID, event.Arguments)
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				toolCalls.SetItem(event.OutputIndex, event.ItemID, event.Item)
			}
		case "response.completed":
			completed = &event.Response
		case "response.failed", "error":
			if event.Error != nil {
				return nil, fmt.Errorf("llm responses stream failed: %s", event.Error.Message)
			}
			return nil, fmt.Errorf("llm responses stream failed: %s", data)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	message := Message{Role: RoleAssistant, Content: full.String(), ToolCalls: toolCalls.List()}
	if completed != nil {
		completedMessage := responseToMessage(*completed)
		if strings.TrimSpace(MessageContentString(message)) == "" {
			message.Content = completedMessage.Content
		}
		if len(message.ToolCalls) == 0 {
			message.ToolCalls = completedMessage.ToolCalls
		}
	}
	return &ChatResponse{Message: message}, nil
}

func (c *OpenAIClient) responsesPayload(req ChatRequest, stream bool) responsesRequest {
	return responsesRequest{
		Model:  c.model,
		Input:  responsesInput(req.Messages),
		Tools:  responsesTools(req.Tools, c.hostedTools),
		Stream: stream,
	}
}

func (c *OpenAIClient) responsesURL() string {
	return c.baseURL + "/responses"
}

func (c *OpenAIClient) logPayload(kind string, payload responsesRequest) {
	if !c.logRequest {
		return
	}
	slog.Info("llm request",
		"kind", kind,
		"url", c.responsesURL(),
		"model", payload.Model,
		"tools", len(payload.Tools),
		"stream", payload.Stream,
		"input", summarizeResponsesInput(payload.Input),
	)
}

func normalizeHostedTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	seen := make(map[string]struct{})
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		out = append(out, tool)
	}
	return out
}

func responsesInput(messages []Message) []any {
	items := make([]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == RoleTool {
			content := ContentString(msg.Content)
			if msg.ToolCallID == "" {
				items = append(items, responsesMessageInputItem{
					Role:    string(RoleUser),
					Content: "工具结果：\n" + content,
				})
				continue
			}
			items = append(items, responsesFunctionCallOutputInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: content,
			})
			continue
		}

		content := MessageContentString(msg)
		if msg.Role != RoleAssistant || strings.TrimSpace(content) != "" {
			items = append(items, responsesMessageInputItem{
				Role:    string(msg.Role),
				Content: responsesContent(msg.Content),
			})
		}
		if msg.Role == RoleAssistant {
			for _, call := range msg.ToolCalls {
				items = append(items, responsesFunctionCallInputItem{
					Type:      "function_call",
					CallID:    call.ID,
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				})
			}
		}
	}
	return items
}

func responsesContent(content any) any {
	switch value := content.(type) {
	case []ContentPart:
		parts := make([]responsesContentPart, 0, len(value))
		for _, part := range value {
			switch part.Type {
			case "text":
				parts = append(parts, responsesContentPart{Type: "input_text", Text: part.Text})
			case "image_url":
				if part.ImageURL == nil {
					continue
				}
				parts = append(parts, responsesContentPart{Type: "input_image", ImageURL: part.ImageURL.URL})
			default:
				if part.Text != "" {
					parts = append(parts, responsesContentPart{Type: "input_text", Text: part.Text})
				}
			}
		}
		return parts
	case string:
		return value
	case nil:
		return ""
	default:
		return fmt.Sprint(value)
	}
}

func responsesTools(localTools []Tool, hostedTools []string) []map[string]any {
	if len(localTools) == 0 && len(hostedTools) == 0 {
		return nil
	}
	tools := make([]map[string]any, 0, len(hostedTools)+len(localTools))
	for _, toolType := range hostedTools {
		tools = append(tools, map[string]any{"type": toolType})
	}
	for _, item := range localTools {
		if item.Type != "function" {
			continue
		}
		parameters := item.Function.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object"}
		}
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        item.Function.Name,
			"description": item.Function.Description,
			"parameters":  parameters,
		})
	}
	return tools
}

func responseToMessage(resp responsesResponse) Message {
	var text strings.Builder
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Text != "" {
				text.WriteString(part.Text)
			} else if part.Refusal != "" {
				text.WriteString(part.Refusal)
			}
		}
	}
	if text.Len() == 0 && resp.OutputText != "" {
		text.WriteString(resp.OutputText)
	}

	toolCalls := make([]ToolCall, 0)
	for _, item := range resp.Output {
		if item.Type != "function_call" {
			continue
		}
		toolCalls = append(toolCalls, responseFunctionCallToToolCall(item))
	}

	return Message{Role: RoleAssistant, Content: text.String(), ToolCalls: toolCalls}
}

func responseFunctionCallToToolCall(item responsesOutputItem) ToolCall {
	id := item.CallID
	if id == "" {
		id = item.ID
	}
	return ToolCall{
		ID:   id,
		Type: "function",
		Function: FunctionCall{
			Name:      item.Name,
			Arguments: item.Arguments,
		},
	}
}

func summarizeResponsesInput(input []any) []map[string]any {
	summaries := make([]map[string]any, 0, len(input))
	for _, item := range input {
		switch value := item.(type) {
		case responsesMessageInputItem:
			summaries = append(summaries, map[string]any{
				"role":    value.Role,
				"content": summarizeContent(value.Content),
			})
		case responsesFunctionCallInputItem:
			summaries = append(summaries, map[string]any{
				"type":      value.Type,
				"call_id":   value.CallID,
				"name":      value.Name,
				"arguments": truncate(value.Arguments, 300),
			})
		case responsesFunctionCallOutputInputItem:
			summaries = append(summaries, map[string]any{
				"type":    value.Type,
				"call_id": value.CallID,
				"output":  truncate(value.Output, 300),
			})
		default:
			summaries = append(summaries, map[string]any{"type": fmt.Sprintf("%T", item)})
		}
	}
	return summaries
}

func summarizeContent(content any) any {
	switch value := content.(type) {
	case string:
		if len([]rune(value)) > 300 {
			return string([]rune(value)[:300]) + "..."
		}
		return value
	case []responsesContentPart:
		out := make([]map[string]any, 0, len(value))
		for _, part := range value {
			item := map[string]any{"type": part.Type}
			if part.Text != "" {
				item["text"] = truncate(part.Text, 300)
			}
			if part.ImageURL != "" {
				item["image_url"] = summarizeURL(part.ImageURL)
			}
			out = append(out, item)
		}
		return out
	case []ContentPart:
		out := make([]map[string]any, 0, len(value))
		for _, part := range value {
			item := map[string]any{"type": part.Type}
			if part.Text != "" {
				item["text"] = truncate(part.Text, 300)
			}
			if part.ImageURL != nil {
				item["image_url"] = summarizeURL(part.ImageURL.URL)
			}
			out = append(out, item)
		}
		return out
	default:
		return fmt.Sprintf("%T", content)
	}
}

func summarizeURL(value string) string {
	if strings.HasPrefix(value, "data:") {
		return truncate(value, 80) + fmt.Sprintf(" (len=%d)", len(value))
	}
	return value
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

type responsesRequest struct {
	Model  string           `json:"model"`
	Input  []any            `json:"input"`
	Tools  []map[string]any `json:"tools,omitempty"`
	Stream bool             `json:"stream,omitempty"`
}

type responsesMessageInputItem struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type responsesFunctionCallInputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutputInputItem struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type responsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesResponse struct {
	ID         string                `json:"id"`
	Output     []responsesOutputItem `json:"output"`
	OutputText string                `json:"output_text"`
	Error      *responsesError       `json:"error"`
}

type responsesOutputItem struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id"`
	CallID    string                   `json:"call_id"`
	Name      string                   `json:"name"`
	Arguments string                   `json:"arguments"`
	Role      string                   `json:"role"`
	Status    string                   `json:"status"`
	Content   []responsesOutputContent `json:"content"`
}

type responsesOutputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responsesError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

type responsesStreamEvent struct {
	Type        string              `json:"type"`
	Delta       string              `json:"delta"`
	Arguments   string              `json:"arguments"`
	ItemID      string              `json:"item_id"`
	OutputIndex int                 `json:"output_index"`
	Item        responsesOutputItem `json:"item"`
	Response    responsesResponse   `json:"response"`
	Error       *responsesError     `json:"error"`
}

type responsesStreamToolCallAccumulator struct {
	items map[string]*ToolCall
	order []string
}

func newResponsesStreamToolCallAccumulator() *responsesStreamToolCallAccumulator {
	return &responsesStreamToolCallAccumulator{items: make(map[string]*ToolCall)}
}

func (a *responsesStreamToolCallAccumulator) AddArgumentsDelta(index int, itemID string, delta string) {
	if delta == "" {
		return
	}
	item := a.ensure(index, itemID)
	item.Function.Arguments += delta
}

func (a *responsesStreamToolCallAccumulator) SetArguments(index int, itemID string, arguments string) {
	if arguments == "" {
		return
	}
	item := a.ensure(index, itemID)
	item.Function.Arguments = arguments
}

func (a *responsesStreamToolCallAccumulator) SetItem(index int, itemID string, output responsesOutputItem) {
	fallbackKey := itemKey(index, itemID)
	key := itemKey(index, firstNonEmpty(itemID, output.ID, output.CallID))
	a.mergeKey(fallbackKey, key)
	item := a.ensureWithKey(key)
	item.ID = firstNonEmpty(output.CallID, output.ID, item.ID)
	item.Type = "function"
	if output.Name != "" {
		item.Function.Name = output.Name
	}
	if output.Arguments != "" {
		item.Function.Arguments = output.Arguments
	}
}

func (a *responsesStreamToolCallAccumulator) List() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, key := range a.order {
		item := *a.items[key]
		if item.Type == "" {
			item.Type = "function"
		}
		if item.ID == "" {
			item.ID = key
		}
		if item.Function.Name == "" && item.Function.Arguments == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (a *responsesStreamToolCallAccumulator) ensure(index int, itemID string) *ToolCall {
	return a.ensureWithKey(itemKey(index, itemID))
}

func (a *responsesStreamToolCallAccumulator) ensureWithKey(key string) *ToolCall {
	item, ok := a.items[key]
	if !ok {
		a.order = append(a.order, key)
		item = &ToolCall{Type: "function"}
		a.items[key] = item
	}
	return item
}

func (a *responsesStreamToolCallAccumulator) mergeKey(from string, to string) {
	if from == to {
		return
	}
	fromItem, hasFrom := a.items[from]
	if !hasFrom {
		return
	}
	toItem, hasTo := a.items[to]
	if !hasTo {
		a.items[to] = fromItem
		delete(a.items, from)
		for i, key := range a.order {
			if key == from {
				a.order[i] = to
				return
			}
		}
		a.order = append(a.order, to)
		return
	}
	if toItem.ID == "" {
		toItem.ID = fromItem.ID
	}
	if toItem.Type == "" {
		toItem.Type = fromItem.Type
	}
	if toItem.Function.Name == "" {
		toItem.Function.Name = fromItem.Function.Name
	}
	if toItem.Function.Arguments == "" {
		toItem.Function.Arguments = fromItem.Function.Arguments
	}
	delete(a.items, from)
	for i, key := range a.order {
		if key == from {
			a.order = append(a.order[:i], a.order[i+1:]...)
			return
		}
	}
}

func itemKey(index int, itemID string) string {
	if itemID != "" {
		return itemID
	}
	return fmt.Sprintf("output_%d", index)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type streamToolCallAccumulator struct {
	items map[int]*ToolCall
	order []int
}

func newStreamToolCallAccumulator() *streamToolCallAccumulator {
	return &streamToolCallAccumulator{items: make(map[int]*ToolCall)}
}

func (a *streamToolCallAccumulator) Add(chunks []struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}) {
	for _, chunk := range chunks {
		item, ok := a.items[chunk.Index]
		if !ok {
			a.order = append(a.order, chunk.Index)
			item = &ToolCall{}
			a.items[chunk.Index] = item
		}
		if chunk.ID != "" {
			item.ID = chunk.ID
		}
		if chunk.Type != "" {
			item.Type = chunk.Type
		}
		if chunk.Function.Name != "" {
			item.Function.Name += chunk.Function.Name
		}
		if chunk.Function.Arguments != "" {
			item.Function.Arguments += chunk.Function.Arguments
		}
	}
}

func (a *streamToolCallAccumulator) List() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for _, index := range a.order {
		item := *a.items[index]
		if item.Type == "" {
			item.Type = "function"
		}
		if item.ID == "" {
			item.ID = fmt.Sprintf("call_%d", index)
		}
		if item.Function.Name == "" && item.Function.Arguments == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}
