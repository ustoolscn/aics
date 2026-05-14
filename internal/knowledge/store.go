package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Store struct {
	db *sql.DB
}

type SearchResult struct {
	SourcePath string
	ChunkIndex int
	Title      string
	Content    string
	Distance   float64
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) EnsureSchema(ctx context.Context, dimensions int) error {
	if dimensions <= 0 {
		dimensions = 768
	}
	statements := []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS knowledge_chunks (
			id bigserial PRIMARY KEY,
			source_path text NOT NULL,
			chunk_index int NOT NULL,
			title text NOT NULL DEFAULT 'none',
			content text NOT NULL,
			embedding vector(%d) NOT NULL,
			content_hash text NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now(),
			UNIQUE (source_path, chunk_index)
		)`, dimensions),
		`CREATE INDEX IF NOT EXISTS knowledge_chunks_embedding_hnsw_idx
			ON knowledge_chunks
			USING hnsw (embedding vector_cosine_ops)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertChunk(ctx context.Context, chunk Chunk, vector []float32) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO knowledge_chunks
		(source_path, chunk_index, title, content, embedding, content_hash, updated_at)
		VALUES ($1, $2, $3, $4, $5::vector, $6, $7)
		ON CONFLICT (source_path, chunk_index) DO UPDATE SET
			title = EXCLUDED.title,
			content = EXCLUDED.content,
			embedding = EXCLUDED.embedding,
			content_hash = EXCLUDED.content_hash,
			updated_at = EXCLUDED.updated_at`,
		chunk.SourcePath,
		chunk.Index,
		emptyDefault(chunk.Title, "none"),
		chunk.Content,
		VectorLiteral(vector),
		HashContent(chunk.Content),
		time.Now(),
	)
	return err
}

func (s *Store) DeleteMissing(ctx context.Context, keep map[string]struct{}) error {
	rows, err := s.db.QueryContext(ctx, `SELECT source_path, chunk_index FROM knowledge_chunks`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stale []struct {
		path  string
		index int
	}
	for rows.Next() {
		var path string
		var index int
		if err := rows.Scan(&path, &index); err != nil {
			return err
		}
		if _, ok := keep[ChunkKey(path, index)]; !ok {
			stale = append(stale, struct {
				path  string
				index int
			}{path: path, index: index})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, item := range stale {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM knowledge_chunks WHERE source_path = $1 AND chunk_index = $2`, item.path, item.index); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Search(ctx context.Context, vector []float32, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `SELECT source_path, chunk_index, title, content, embedding <=> $1::vector AS distance
		FROM knowledge_chunks
		ORDER BY embedding <=> $1::vector
		LIMIT $2`, VectorLiteral(vector), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var item SearchResult
		if err := rows.Scan(&item.SourcePath, &item.ChunkIndex, &item.Title, &item.Content, &item.Distance); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

func VectorLiteral(vector []float32) string {
	parts := make([]string, 0, len(vector))
	for _, value := range vector {
		parts = append(parts, strconv.FormatFloat(float64(value), 'f', -1, 32))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func ChunkKey(path string, index int) string {
	return path + "#" + strconv.Itoa(index)
}

func emptyDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
