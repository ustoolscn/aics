package imagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"aics/internal/config"
)

type Client struct {
	uploadURL       string
	authHeader      string
	authValue       string
	fieldName       string
	responseURLPath string
	httpClient      *http.Client
}

func NewClient(cfg config.ImageHostConfig) *Client {
	return &Client{
		uploadURL:       cfg.UploadURL,
		authHeader:      cfg.AuthHeader,
		authValue:       cfg.AuthValue,
		fieldName:       cfg.FieldName,
		responseURLPath: cfg.ResponseURLPath,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) Enabled() bool {
	return strings.TrimSpace(c.uploadURL) != ""
}

func (c *Client) Upload(ctx context.Context, fileName string, data []byte) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("image host upload url is empty")
	}
	fieldName := c.fieldName
	if fieldName == "" {
		fieldName = "file"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.uploadURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.authHeader != "" && c.authValue != "" {
		req.Header.Set(c.authHeader, c.authValue)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("image host upload failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	url, err := extractJSONPath(respBody, c.responseURLPath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("image host response url is empty")
	}
	return url, nil
}

func extractJSONPath(data []byte, path string) (string, error) {
	if path == "" {
		path = "url"
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	current := payload
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("image host response path %q is not an object", part)
		}
		current, ok = object[part]
		if !ok {
			return "", fmt.Errorf("image host response missing path %q", path)
		}
	}
	value, ok := current.(string)
	if !ok {
		return "", fmt.Errorf("image host response path %q is not string", path)
	}
	return value, nil
}
