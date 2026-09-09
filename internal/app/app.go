package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/itsmostafa/qi/internal/config"
	"github.com/itsmostafa/qi/internal/db"
	"github.com/itsmostafa/qi/internal/indexer"
	"github.com/itsmostafa/qi/internal/providers"
	"github.com/itsmostafa/qi/internal/search"
)

// App wires config, db, and services together.
type App struct {
	Config   *config.Config
	DB       *db.DB
	Indexer  *indexer.Indexer
	Embedder *indexer.Embedder // nil if no embedding provider configured
	BM25     *search.BM25
	Vector   *search.VectorSearch
	Hybrid   *search.Hybrid
}

// New opens the database and wires all services.
func New(ctx context.Context, cfgPath string) (*App, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	database, err := db.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	// Best-effort: the rename is a migration, not a precondition. A set that
	// cannot be applied rolls back whole, and warning here keeps every other
	// command — including the `qi delete` that resolves the conflict — usable.
	if err := database.RenameCollections(ctx, legacyRenames(cfg.Collections)); err != nil {
		slog.Warn("collection names left unnormalized", "error", err)
	}

	// The embedding fingerprint identifies which provider endpoint and
	// model/dimension/truncation config produced a stored vector. Computed once here so the embedder
	// (writer) and vector search (reader) always agree on the active
	// identity. Empty when no embedding provider is configured.
	fingerprint := cfg.Providers.Embedding.Fingerprint()

	a := &App{
		Config:  cfg,
		DB:      database,
		Indexer: indexer.New(database, cfg.Search.ChunkSize),
		BM25:    search.NewBM25(database),
		Vector:  search.NewVectorSearch(database, fingerprint),
	}

	if cfg.Providers.Embedding != nil {
		embProvider := providers.NewEmbedding(cfg.Providers.Embedding)
		providerTag := cfg.Providers.Embedding.Name
		if providerTag == "" {
			providerTag = "http"
		}
		a.Embedder = indexer.NewEmbedder(database, embProvider, providerTag, fingerprint)
		// Persist on the same boundary the provider requests on.
		a.Embedder.BatchSize = cfg.Providers.Embedding.BatchSize
		a.Hybrid = search.NewHybrid(a.BM25, a.Vector, embProvider, cfg.Search)
	}

	return a, nil
}

// Close releases all resources.
func (a *App) Close() error {
	return a.DB.Close()
}

func legacyRenames(collections []config.Collection) [][2]string {
	stableNames := map[string]bool{}
	legacyNameCounts := map[string]int{}
	for _, col := range collections {
		if col.OriginalName == "" || col.OriginalName == col.Name {
			// Nothing moves out of this name, so its rows stay its own.
			stableNames[col.Name] = true
			continue
		}
		legacyNameCounts[col.OriginalName]++
	}

	renames := make([][2]string, 0, len(collections))
	for _, col := range collections {
		if col.OriginalName == "" || col.OriginalName == col.Name {
			continue
		}
		// Two collections claiming one legacy name, or a legacy name that is
		// also a collection's settled name: the rows under it are ambiguous, so
		// leave them where they are.
		if legacyNameCounts[col.OriginalName] > 1 || stableNames[col.OriginalName] {
			continue
		}
		renames = append(renames, [2]string{col.OriginalName, col.Name})
	}
	return renames
}
