package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GeminiClient struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	httpClient *http.Client
}

func NewGeminiClient(baseURL, apiKey, model string, dimensions int, timeout time.Duration) *GeminiClient {
	if model == "" {
		model = "gemini-embedding-2"
	}
	if dimensions <= 0 {
		dimensions = 768
	}
	return &GeminiClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		model:      model,
		dimensions: dimensions,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *GeminiClient) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, fmt.Errorf("gemini api key is empty")
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("embedding text is empty")
	}

	body, err := json.Marshal(c.requestPayload(text))
	if err != nil {
		return nil, err
	}

	url := c.endpointURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.baseURL == "" {
		req.Header.Set("x-goog-api-key", c.apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("x-goog-api-key", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gemini embedding failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var decoded geminiEmbedResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Embedding.Values) > 0 {
		return decoded.Embedding.Values, nil
	}
	if len(decoded.Data) > 0 && len(decoded.Data[0].Embedding) > 0 {
		return decoded.Data[0].Embedding, nil
	}
	if len(decoded.Embeddings) == 0 || len(decoded.Embeddings[0].Values) == 0 {
		return nil, fmt.Errorf("gemini embedding response has no values")
	}
	return decoded.Embeddings[0].Values, nil
}

func (c *GeminiClient) endpointURL() string {
	if c.baseURL != "" {
		return fmt.Sprintf("%s/models/%s:embedContent", c.baseURL, c.model)
	}
	return fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:embedContent", c.model)
}

func (c *GeminiClient) requestPayload(text string) any {
	return geminiEmbedRequest{
		Model: "models/" + c.model,
		Content: geminiContent{
			Parts: []geminiPart{{Text: text}},
		},
		OutputDimensionality:      c.dimensions,
		OutputDimensionalityCamel: c.dimensions,
	}
}

type geminiEmbedRequest struct {
	Model                     string        `json:"model,omitempty"`
	Content                   geminiContent `json:"content"`
	OutputDimensionality      int           `json:"output_dimensionality,omitempty"`
	OutputDimensionalityCamel int           `json:"outputDimensionality,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiEmbedResponse struct {
	Embedding struct {
		Values []float32 `json:"values"`
	} `json:"embedding"`
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}
