package knowledge

import (
	"context"
	"fmt"

	"aics/internal/embedding"
)

type Indexer struct {
	store    *Store
	embedder embedding.Client
}

type IndexOptions struct {
	Dir         string
	ChunkSize   int
	Overlap     int
	Dimensions  int
	DeleteStale bool
}

type IndexResult struct {
	Documents int
	Chunks    int
}

func NewIndexer(store *Store, embedder embedding.Client) *Indexer {
	return &Indexer{store: store, embedder: embedder}
}

func (i *Indexer) Index(ctx context.Context, opts IndexOptions) (IndexResult, error) {
	if err := i.store.EnsureSchema(ctx, opts.Dimensions); err != nil {
		return IndexResult{}, err
	}

	docs, err := LoadDocuments(opts.Dir)
	if err != nil {
		return IndexResult{}, err
	}
	chunks := ChunkDocuments(docs, opts.ChunkSize, opts.Overlap)
	keep := make(map[string]struct{}, len(chunks))

	for _, chunk := range chunks {
		input := embedding.FormatDocument(chunk.Title, chunk.Content)
		vector, err := i.embedder.Embed(ctx, input)
		if err != nil {
			return IndexResult{}, fmt.Errorf("embed %s chunk %d: %w", chunk.SourcePath, chunk.Index, err)
		}
		if err := i.store.UpsertChunk(ctx, chunk, vector); err != nil {
			return IndexResult{}, fmt.Errorf("upsert %s chunk %d: %w", chunk.SourcePath, chunk.Index, err)
		}
		keep[ChunkKey(chunk.SourcePath, chunk.Index)] = struct{}{}
	}

	if opts.DeleteStale {
		if err := i.store.DeleteMissing(ctx, keep); err != nil {
			return IndexResult{}, err
		}
	}

	return IndexResult{Documents: len(docs), Chunks: len(chunks)}, nil
}
