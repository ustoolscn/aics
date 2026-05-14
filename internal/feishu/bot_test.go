package feishu

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractTextFromTopicPostContent(t *testing.T) {
	content := `{
		"title":"登录问题",
		"content":[[
			{"tag":"text","text":"我登录不上"},
			{"tag":"text","text":"提示验证码错误"}
		]]
	}`

	got := extractText(content, "post")
	if !strings.Contains(got, "登录问题") {
		t.Fatalf("missing title: %q", got)
	}
	if !strings.Contains(got, "我登录不上") || !strings.Contains(got, "提示验证码错误") {
		t.Fatalf("missing post text: %q", got)
	}
}

func TestExtractTextFromPlainTextContent(t *testing.T) {
	got := extractText(`{"text":"hello"}`, "text")
	if got != "hello" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestExtractImageKeys(t *testing.T) {
	content := `{
		"image_key":"img_a",
		"content":[[
			{"tag":"img","image_key":"img_b"},
			{"tag":"text","text":"hello"}
		]]
	}`
	got := extractImageKeys(content)
	if len(got) != 2 || got[0] != "img_a" || got[1] != "img_b" {
		t.Fatalf("unexpected image keys: %#v", got)
	}
}

func TestExtractImageURLs(t *testing.T) {
	content := `{
		"url":"https://example.com/a.png",
		"content":[[
			{"tag":"img","image_url":"https://example.com/b.png"}
		]]
	}`
	got := extractImageURLs(content)
	if len(got) != 2 || got[0] != "https://example.com/a.png" || got[1] != "https://example.com/b.png" {
		t.Fatalf("unexpected image urls: %#v", got)
	}
}

func TestLooksLikeImageOnlyJSON(t *testing.T) {
	if !looksLikeImageOnlyJSON(`{"image_key":"img_v3_xxx"}`) {
		t.Fatal("expected image-only json")
	}
	if looksLikeImageOnlyJSON(`{"image_key":"img_v3_xxx","text":"hello"}`) {
		t.Fatal("expected mixed json to be false")
	}
}

func TestImageDataURL(t *testing.T) {
	got := imageDataURL("image/png", []byte("abc"))
	if got != "data:image/png;base64,YWJj" {
		t.Fatalf("unexpected data url: %s", got)
	}
}

func TestMarkdownCardContentUsesJSON2RichText(t *testing.T) {
	content, err := markdownCardContent("# 标题\n\n| A | B |\n| - | - |\n| 1 | 2 |")
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]any
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatal(err)
	}
	if card["schema"] != "2.0" {
		t.Fatalf("expected schema 2.0, got %#v", card["schema"])
	}
	body, ok := card["body"].(map[string]any)
	if !ok {
		t.Fatalf("missing body: %#v", card)
	}
	elements, ok := body["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("unexpected body elements: %#v", body["elements"])
	}
	element, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected element: %#v", elements[0])
	}
	if element["tag"] != "markdown" || element["element_id"] != "answer" {
		t.Fatalf("unexpected markdown element: %#v", element)
	}
}

func TestWriteChallengeResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	ok := writeChallengeResponse(rec, []byte(`{"challenge":"abc123"}`))
	if !ok {
		t.Fatal("expected challenge to be handled")
	}
	if rec.Code != 200 {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"challenge":"abc123"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
