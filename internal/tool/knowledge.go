package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"aics/internal/embedding"
	"aics/internal/knowledge"
)

type KnowledgeSearch struct {
	dir string
}

func NewKnowledgeSearch(dir string) *KnowledgeSearch {
	return &KnowledgeSearch{dir: dir}
}

type RAGKnowledgeSearch struct {
	store    *knowledge.Store
	embedder embedding.Client
	topK     int
}

func NewRAGKnowledgeSearch(store *knowledge.Store, embedder embedding.Client, topK int) *RAGKnowledgeSearch {
	if topK <= 0 {
		topK = 5
	}
	return &RAGKnowledgeSearch{store: store, embedder: embedder, topK: topK}
}

func (k *KnowledgeSearch) Name() string {
	return "knowledge_search"
}

func (k *KnowledgeSearch) Description() string {
	return "查询本地知识库，适合产品说明、常见问题、流程规则、售后政策等问题。"
}

func (k *KnowledgeSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "要查询的问题或关键词。",
			},
		},
		"required": []string{"query"},
	}
}

func (k *RAGKnowledgeSearch) Name() string {
	return "knowledge_search"
}

func (k *RAGKnowledgeSearch) Description() string {
	return "查询知识库，适合产品说明、常见问题、流程规则、售后政策等问题。"
}

func (k *RAGKnowledgeSearch) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "要查询的问题或关键词。",
			},
		},
		"required": []string{"query"},
	}
}

func (k *KnowledgeSearch) Call(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "知识库查询为空。", nil
	}

	docs, err := loadMarkdownDocs(k.dir)
	if err != nil {
		return "", err
	}
	if len(docs) == 0 {
		return "知识库为空。", nil
	}

	queryTerms := terms(query)
	type scoredDoc struct {
		path    string
		content string
		score   int
	}
	var scored []scoredDoc
	for _, doc := range docs {
		score := 0
		lower := strings.ToLower(doc.content)
		for _, term := range queryTerms {
			score += strings.Count(lower, term)
		}
		if score > 0 {
			scored = append(scored, scoredDoc{path: doc.path, content: doc.content, score: score})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	if len(scored) == 0 {
		return "没有检索到相关知识库内容。", nil
	}
	if len(scored) > 3 {
		scored = scored[:3]
	}

	var b strings.Builder
	for i, doc := range scored {
		content := strings.TrimSpace(doc.content)
		if len([]rune(content)) > 1200 {
			content = string([]rune(content)[:1200]) + "..."
		}
		fmt.Fprintf(&b, "来源 %d: %s\n%s\n\n", i+1, doc.path, content)
	}
	return strings.TrimSpace(b.String()), nil
}

func (k *RAGKnowledgeSearch) Call(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "知识库查询为空。", nil
	}

	vector, err := k.embedder.Embed(ctx, embedding.FormatQuery(query))
	if err != nil {
		return "", err
	}
	results, err := k.store.Search(ctx, vector, k.topK)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return "没有检索到相关知识库内容。", nil
	}

	var b strings.Builder
	for i, item := range results {
		content := strings.TrimSpace(item.Content)
		if len([]rune(content)) > 1200 {
			content = string([]rune(content)[:1200]) + "..."
		}
		fmt.Fprintf(&b, "来源 %d: %s#chunk-%d\n标题: %s\n相关度距离: %.4f\n%s\n\n",
			i+1,
			item.SourcePath,
			item.ChunkIndex,
			item.Title,
			item.Distance,
			content,
		)
	}
	return strings.TrimSpace(b.String()), nil
}

type markdownDoc struct {
	path    string
	content string
}

func loadMarkdownDocs(dir string) ([]markdownDoc, error) {
	var docs []markdownDoc
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		docs = append(docs, markdownDoc{path: path, content: string(data)})
		return nil
	})
	return docs, err
}

func terms(query string) []string {
	query = strings.ToLower(query)
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{})
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}
