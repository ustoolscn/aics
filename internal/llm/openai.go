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
	baseURL    string
	apiKey     string
	model      string
	logRequest bool
	httpClient *http.Client
}

func NewOpenAIClient(baseURL, apiKey, model string, timeout time.Duration, logRequest bool) *OpenAIClient {
	return &OpenAIClient{
		baseURL:    baseURL,
		apiKey:     apiKey,
		model:      model,
		logRequest: logRequest,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *OpenAIClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	payload := chatCompletionRequest{
		Model:    c.model,
		Messages: req.Messages,
		Tools:    req.Tools,
	}
	if len(req.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	c.logPayload("chat", payload)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
		return nil, fmt.Errorf("llm request failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("llm response has no choices")
	}

	return &ChatResponse{Message: decoded.Choices[0].Message}, nil
}

func (c *OpenAIClient) ChatStream(ctx context.Context, req ChatRequest, onDelta func(delta string) error) (*ChatResponse, error) {
	payload := chatCompletionRequest{
		Model:    c.model,
		Messages: req.Messages,
		Tools:    req.Tools,
		Stream:   true,
	}
	if len(req.Tools) > 0 {
		payload.ToolChoice = "auto"
	}
	c.logPayload("chat_stream", payload)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
		return nil, fmt.Errorf("llm stream request failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var full bytes.Buffer
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

		var chunk chatCompletionStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, err
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			full.WriteString(choice.Delta.Content)
			if onDelta != nil {
				if err := onDelta(choice.Delta.Content); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return &ChatResponse{Message: Message{Role: RoleAssistant, Content: full.String()}}, nil
}

func (c *OpenAIClient) logPayload(kind string, payload chatCompletionRequest) {
	if !c.logRequest {
		return
	}
	summaries := make([]map[string]any, 0, len(payload.Messages))
	for _, msg := range payload.Messages {
		summaries = append(summaries, map[string]any{
			"role":    msg.Role,
			"content": summarizeContent(msg.Content),
		})
	}
	slog.Info("llm request",
		"kind", kind,
		"url", c.baseURL+"/chat/completions",
		"model", payload.Model,
		"tools", len(payload.Tools),
		"stream", payload.Stream,
		"messages", summaries,
	)
}

func summarizeContent(content any) any {
	switch value := content.(type) {
	case string:
		if len([]rune(value)) > 300 {
			return string([]rune(value)[:300]) + "..."
		}
		return value
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

type chatCompletionRequest struct {
	Model      string    `json:"model"`
	Messages   []Message `json:"messages"`
	Tools      []Tool    `json:"tools,omitempty"`
	ToolChoice any       `json:"tool_choice,omitempty"`
	Stream     bool      `json:"stream,omitempty"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type chatCompletionStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}
