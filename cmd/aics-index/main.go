package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"aics/internal/config"
	"aics/internal/embedding"
	"aics/internal/knowledge"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	_ = config.LoadDotEnv(".env")

	databaseURL := os.Getenv("DATABASE_URL")
	embeddingAPIKey := getenv("EMBEDDING_API_KEY", os.Getenv("GEMINI_API_KEY"))
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if embeddingAPIKey == "" {
		log.Fatal("EMBEDDING_API_KEY is required")
	}

	ctx := context.Background()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}

	knowledgeDir := getenv("KNOWLEDGE_DIR", "knowledge")
	baseURL := strings.TrimRight(os.Getenv("EMBEDDING_BASE_URL"), "/")
	model := getenv("EMBEDDING_MODEL", "gemini-embedding-2")
	dimensions := getenvInt("EMBEDDING_DIMENSIONS", 768)
	chunkSize := getenvInt("KNOWLEDGE_CHUNK_SIZE", 900)
	overlap := getenvInt("KNOWLEDGE_CHUNK_OVERLAP", 120)
	timeout := time.Duration(getenvInt("LLM_TIMEOUT_SECONDS", 60)) * time.Second

	store := knowledge.NewStore(db)
	embedder := embedding.NewGeminiClient(baseURL, embeddingAPIKey, model, dimensions, timeout)
	indexer := knowledge.NewIndexer(store, embedder)

	result, err := indexer.Index(ctx, knowledge.IndexOptions{
		Dir:         knowledgeDir,
		ChunkSize:   chunkSize,
		Overlap:     overlap,
		Dimensions:  dimensions,
		DeleteStale: true,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("indexed %d documents into %d chunks with %s (%d dimensions)\n", result.Documents, result.Chunks, model, dimensions)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
