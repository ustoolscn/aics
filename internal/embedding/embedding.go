package embedding

import "context"

type Client interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

func FormatQuery(query string) string {
	return "task: question answering | query: " + query
}

func FormatDocument(title, content string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + content
}
