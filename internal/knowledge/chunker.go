package knowledge

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Document struct {
	Path    string
	Title   string
	Content string
}

type Chunk struct {
	SourcePath string
	Index      int
	Title      string
	Content    string
}

func LoadDocuments(dir string) ([]Document, error) {
	var docs []Document
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
		content := string(data)
		docs = append(docs, Document{
			Path:    path,
			Title:   titleFromContent(path, content),
			Content: content,
		})
		return nil
	})
	return docs, err
}

func ChunkDocuments(docs []Document, chunkSize, overlap int) []Chunk {
	if chunkSize <= 0 {
		chunkSize = 900
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= chunkSize {
		overlap = chunkSize / 5
	}

	var chunks []Chunk
	for _, doc := range docs {
		parts := chunkText(doc.Content, chunkSize, overlap)
		for i, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			chunks = append(chunks, Chunk{
				SourcePath: doc.Path,
				Index:      i,
				Title:      doc.Title,
				Content:    part,
			})
		}
	}
	return chunks
}

func chunkText(content string, chunkSize, overlap int) []string {
	content = normalizeNewlines(content)
	paragraphs := splitParagraphs(content)
	var chunks []string
	var current strings.Builder

	flush := func() {
		text := strings.TrimSpace(current.String())
		if text != "" {
			chunks = append(chunks, text)
		}
		current.Reset()
	}

	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if runeLen(paragraph) > chunkSize {
			flush()
			chunks = append(chunks, splitLongText(paragraph, chunkSize, overlap)...)
			continue
		}
		nextLen := runeLen(current.String()) + runeLen(paragraph) + 2
		if current.Len() > 0 && nextLen > chunkSize {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(paragraph)
	}
	flush()
	return chunks
}

func splitLongText(text string, chunkSize, overlap int) []string {
	runes := []rune(text)
	var chunks []string
	step := chunkSize - overlap
	if step <= 0 {
		step = chunkSize
	}
	for start := 0; start < len(runes); start += step {
		end := start + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

func titleFromContent(path, content string) string {
	re := regexp.MustCompile(`(?m)^#\s+(.+)$`)
	match := re.FindStringSubmatch(content)
	if len(match) == 2 {
		return strings.TrimSpace(match[1])
	}
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func splitParagraphs(content string) []string {
	return regexp.MustCompile(`\n\s*\n`).Split(content, -1)
}

func normalizeNewlines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func runeLen(value string) int {
	return len([]rune(value))
}
