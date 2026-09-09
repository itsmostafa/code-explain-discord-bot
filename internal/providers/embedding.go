package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/itsmostafa/qi/internal/config"
)

// A legitimate response is roughly batch_size × dimension × 10 bytes of JSON;
// 64 MiB leaves room for large batches while capping a runaway body.
const maxResponseBytes = 64 << 20

// DefaultBatchSize is the batch size used when the config leaves it unset.
// The indexer persists on the same boundary, so both must agree.
const DefaultBatchSize = 32

// ErrTransient marks a failure that is worth retrying: a network error, a
// 429, or a 5xx. Callers that can retry (the indexer) check for it; callers
// that fall back instead (query-time embedding) ignore it and stay fast.
var ErrTransient = errors.New("transient embedding failure")

// netErr classifies a network failure mid-request. A cancelled or timed-out
// context is the caller's decision, not a flaky endpoint, and is never marked
// retryable; anything else is.
func netErr(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%s: %w: %w", what, ErrTransient, err)
}

// excerpt returns a short, single-line slice of a response body for use in
// error messages. Fields collapses every run of whitespace, including the
// control characters an HTML error page arrives wrapped in.
func excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "") + "…"
	}
	return s
}

type embeddingProvider struct {
	cfg    *config.EmbeddingProviderConfig
	client *http.Client
}

// NewEmbedding creates an EmbeddingProvider for an OpenAI-compatible /v1/embeddings endpoint.
func NewEmbedding(cfg *config.EmbeddingProviderConfig) EmbeddingProvider {
	return &embeddingProvider{
		cfg:    cfg,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *embeddingProvider) Dimension() int    { return p.cfg.Dimension }
func (p *embeddingProvider) ModelName() string { return p.cfg.Model }

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (p *embeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p.cfg.Dimension <= 0 {
		return nil, fmt.Errorf("embedding dimension must be positive, got %d", p.cfg.Dimension)
	}
	batchSize := p.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}

	// Truncate any texts that exceed the per-text rune limit.
	if p.cfg.MaxInputChars > 0 {
		truncated := make([]string, len(texts))
		for i, t := range texts {
			runes := []rune(t)
			if len(runes) > p.cfg.MaxInputChars {
				slog.Warn("truncating oversized text for embedding",
					"chars", len(runes), "max", p.cfg.MaxInputChars)
				truncated[i] = string(runes[:p.cfg.MaxInputChars])
			} else {
				truncated[i] = t
			}
		}
		texts = truncated
	}

	var all [][]float32
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		embeddings, err := p.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		all = append(all, embeddings...)
	}
	return all, nil
}

func (p *embeddingProvider) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{
		Model: p.cfg.Model,
		Input: texts,
	})
	if err != nil {
		return nil, err
	}

	url := apiBase(p.cfg.BaseURL, "/v1/embeddings")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, netErr(ctx, "embedding request", err)
	}
	defer resp.Body.Close()

	// Read through a bounded reader and check status before decoding: an HTML
	// proxy error used to surface as a JSON parse error, and an unbounded body
	// could be read entirely into memory.
	// A body that stops mid-flight (server disconnect, read timeout) is as
	// retryable as a failed request, including when it truncates a 429/5xx.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, netErr(ctx, "reading embedding response", err)
	}
	if len(respBody) > maxResponseBytes {
		return nil, fmt.Errorf("embedding response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5 {
			return nil, fmt.Errorf("embedding API returned %s: %w: %s", resp.Status, ErrTransient, excerpt(respBody))
		}
		return nil, fmt.Errorf("embedding API returned %s: %s", resp.Status, excerpt(respBody))
	}

	var result embeddingResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decoding embedding response: %w: %s", err, excerpt(respBody))
	}

	if result.Error != nil {
		return nil, fmt.Errorf("embedding API error: %s", result.Error.Message)
	}

	// Sort by index to preserve order. Validate bounds, uniqueness, and
	// dimension so a buggy or incompatible endpoint returns an error instead
	// of crashing qi or letting a corrupt vector into storage.
	embeddings := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(embeddings) {
			return nil, fmt.Errorf("embedding response index %d out of range for %d inputs", d.Index, len(texts))
		}
		if embeddings[d.Index] != nil {
			return nil, fmt.Errorf("embedding response has duplicate index %d", d.Index)
		}
		if len(d.Embedding) != p.cfg.Dimension {
			return nil, fmt.Errorf("embedding at index %d has dimension %d, expected %d", d.Index, len(d.Embedding), p.cfg.Dimension)
		}
		var norm float64
		for j, value := range d.Embedding {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("embedding at index %d contains non-finite value at dimension %d", d.Index, j)
			}
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			return nil, fmt.Errorf("embedding at index %d has zero norm", d.Index)
		}
		embeddings[d.Index] = d.Embedding
	}

	for i, e := range embeddings {
		if e == nil {
			return nil, fmt.Errorf("missing embedding for index %d", i)
		}
	}

	return embeddings, nil
}
