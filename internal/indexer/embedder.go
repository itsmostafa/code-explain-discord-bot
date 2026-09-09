package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/itsmostafa/qi/internal/db"
	"github.com/itsmostafa/qi/internal/providers"
)

// Retry budget for a batch the provider reports as transient. Bounded so a
// down provider fails the run in seconds, not minutes. A var so tests can
// shrink the delay.
const embedAttempts = 3

var embedRetryDelay = 500 * time.Millisecond

// errPersist marks a failure to write to the local database. Unlike a batch
// the provider rejects, a refusing database refuses the next batch too, so the
// run stops instead of paying for provider calls it cannot store.
var errPersist = errors.New("storing embeddings")

// Embedder generates and atomically stores embeddings for chunks whose
// vector/metadata pair is missing, stale, or invalid.
type Embedder struct {
	db          *db.DB
	provider    providers.EmbeddingProvider
	providerTag string
	fingerprint string
	// BatchSize bounds how many chunks are embedded and persisted together.
	// Set it to the provider's configured batch size so the persistence
	// boundary matches the request boundary. Defaults to
	// providers.DefaultBatchSize.
	BatchSize int
}

func NewEmbedder(database *db.DB, provider providers.EmbeddingProvider, providerTag, fingerprint string) *Embedder {
	return &Embedder{db: database, provider: provider, providerTag: providerTag, fingerprint: fingerprint}
}

type chunkRow struct {
	id   int64
	path string
	text string
}

// EmbedCollection repairs every chunk that does not have a valid vector and
// matching current metadata. Each batch is persisted before the next is
// requested, so an interrupted or failed run never repeats network work that
// already succeeded — a rerun only picks up what is still missing.
func (e *Embedder) EmbedCollection(ctx context.Context, collection string) error {
	dimension := e.provider.Dimension()
	if dimension <= 0 {
		return fmt.Errorf("embedding provider dimension must be positive, got %d", dimension)
	}
	pending, err := e.pendingChunks(ctx, collection, dimension)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	batchSize := e.BatchSize
	if batchSize <= 0 {
		batchSize = providers.DefaultBatchSize
	}

	slog.Info("embedding chunks", "count", len(pending), "collection", collection, "batch_size", batchSize)

	var failures []error
	for start := 0; start < len(pending); start += batchSize {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		end := min(start+batchSize, len(pending))
		batch := pending[start:end]

		err := e.embedAndStore(ctx, batch, dimension)
		switch {
		case err == nil:
		case errors.Is(err, providers.ErrTransient), errors.Is(err, errPersist), ctx.Err() != nil:
			// The provider or the database is unavailable, rather than
			// unhappy with this batch. Stop: everything already persisted
			// stays, and a rerun resumes from here.
			return errors.Join(append(failures, err)...)
		default:
			// This batch is bad, not the provider. Record it and keep going
			// so one poison batch cannot block every later one, run after run.
			slog.Warn("embedding batch failed", "chunks", len(batch), "error", err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// embedAndStore embeds one batch, retrying only failures the provider marks
// transient, then persists vector + metadata atomically per chunk.
func (e *Embedder) embedAndStore(ctx context.Context, batch []chunkRow, dimension int) error {
	texts := make([]string, len(batch))
	for i, row := range batch {
		texts[i] = row.text
	}

	var embeddings [][]float32
	var err error
	for attempt := 1; ; attempt++ {
		embeddings, err = e.provider.Embed(ctx, texts)
		if err == nil || attempt == embedAttempts || !errors.Is(err, providers.ErrTransient) {
			break
		}
		delay := embedRetryDelay << (attempt - 1)
		slog.Warn("retrying embedding batch", "attempt", attempt, "delay", delay, "error", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w (last error: %w)", ctx.Err(), err)
		case <-time.After(delay):
		}
	}
	if err != nil {
		return fmt.Errorf("embedding %s: %w", describe(batch), err)
	}
	if len(embeddings) != len(batch) {
		return fmt.Errorf("embedding %s: provider returned %d vectors for %d texts", describe(batch), len(embeddings), len(batch))
	}

	model := e.provider.ModelName()
	var persistErrs []error
	for i, row := range batch {
		if err := e.db.UpsertEmbedding(ctx, row.id, embeddings[i], e.providerTag, model, dimension, e.fingerprint); err != nil {
			persistErrs = append(persistErrs, fmt.Errorf("chunk %d (%s): %w", row.id, row.path, err))
		}
	}
	if len(persistErrs) > 0 {
		return fmt.Errorf("%w: %w", errPersist, errors.Join(persistErrs...))
	}
	return nil
}

// describe names the files a failed batch came from, so the run reports which
// documents are still unembedded. Paths, not chunk ids, because a batch spans
// far fewer files than chunks and a path is what the user can act on; the
// chunks themselves stay pending in the database and a rerun retries them.
func describe(batch []chunkRow) string {
	const maxNamed = 10
	var paths []string
	for _, row := range batch {
		if !slices.Contains(paths, row.path) {
			paths = append(paths, row.path)
		}
	}
	label := fmt.Sprintf("%d chunks in %s", len(batch), strings.Join(paths[:min(len(paths), maxNamed)], ", "))
	if len(paths) > maxNamed {
		label += fmt.Sprintf(" and %d more files", len(paths)-maxNamed)
	}
	return label
}

// pendingChunks returns every chunk of the collection whose vector/metadata
// pair is missing, stale, or invalid, ordered so batches are stable.
func (e *Embedder) pendingChunks(ctx context.Context, collection string, dimension int) ([]chunkRow, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT c.id, d.path, c.text, cv.chunk_id, cv.vector,
		       em.chunk_id, em.dimension, em.fingerprint
		FROM chunks c
		JOIN documents d ON d.id = c.doc_id
		LEFT JOIN chunk_vectors cv ON cv.chunk_id = c.id
		LEFT JOIN embeddings em ON em.chunk_id = c.id
		WHERE d.collection = ? AND d.active = 1
		ORDER BY c.id
	`, collection)
	if err != nil {
		return nil, fmt.Errorf("fetching unembedded chunks: %w", err)
	}
	defer rows.Close()

	var pending []chunkRow
	for rows.Next() {
		var row chunkRow
		var vectorID, metadataID, storedDimension sql.NullInt64
		var blob []byte
		var storedFingerprint sql.NullString
		if err := rows.Scan(&row.id, &row.path, &row.text, &vectorID, &blob, &metadataID, &storedDimension, &storedFingerprint); err != nil {
			return nil, fmt.Errorf("scanning embeddings to repair: %w", err)
		}
		valid := vectorID.Valid && metadataID.Valid && storedDimension.Valid &&
			int(storedDimension.Int64) == dimension && storedFingerprint.Valid &&
			storedFingerprint.String == e.fingerprint && db.ValidateEmbeddingBlob(blob, dimension) == nil
		if !valid {
			pending = append(pending, row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading embeddings to repair: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing embeddings to repair: %w", err)
	}
	return pending, nil
}
