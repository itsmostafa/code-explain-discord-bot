package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itsmostafa/qi/internal/config"
)

func TestEmbeddingProvider_Embed(t *testing.T) {
	// Mock /v1/embeddings server
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Return fake embeddings
		resp := embeddingResponse{}
		for i := range req.Input {
			vec := make([]float32, 4)
			vec[i%4] = 1.0
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: vec, Index: i})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := &config.EmbeddingProviderConfig{
		BaseURL:   srv.URL,
		Model:     "test-model",
		Dimension: 4,
		BatchSize: 10,
	}
	p := NewEmbedding(cfg)

	texts := []string{"hello", "world", "foo"}
	embeddings, err := p.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(embeddings) != len(texts) {
		t.Errorf("expected %d embeddings, got %d", len(texts), len(embeddings))
	}
	for i, e := range embeddings {
		if len(e) != 4 {
			t.Errorf("embedding[%d] has wrong dimension: %d", i, len(e))
		}
	}
}

func TestEmbeddingProvider_Dimension(t *testing.T) {
	cfg := &config.EmbeddingProviderConfig{Dimension: 768, Model: "m", BaseURL: "http://localhost"}
	p := NewEmbedding(cfg)
	if p.Dimension() != 768 {
		t.Errorf("expected 768, got %d", p.Dimension())
	}
}

func TestEmbeddingProvider_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embeddingResponse{
			Error: &struct {
				Message string `json:"message"`
			}{Message: "model not found"},
		})
	}))
	defer srv.Close()

	cfg := &config.EmbeddingProviderConfig{BaseURL: srv.URL, Model: "bad", Dimension: 4}
	p := NewEmbedding(cfg)
	_, err := p.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Error("expected error for API error response")
	}
}

func TestEmbeddingProvider_SendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp := embeddingResponse{}
		resp.Data = append(resp.Data, struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{Embedding: []float32{1.0, 0.0}, Index: 0})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := &config.EmbeddingProviderConfig{
		BaseURL:   srv.URL,
		Model:     "test-model",
		Dimension: 2,
		APIKey:    "sk-test-key",
	}
	p := NewEmbedding(cfg)
	_, err := p.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("expected Authorization: Bearer sk-test-key, got %q", gotAuth)
	}
}

func TestEmbeddingProvider_NoAuthHeader_WhenKeyEmpty(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp := embeddingResponse{}
		resp.Data = append(resp.Data, struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{Embedding: []float32{1.0, 0.0}, Index: 0})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := &config.EmbeddingProviderConfig{
		BaseURL:   srv.URL,
		Model:     "test-model",
		Dimension: 2,
	}
	p := NewEmbedding(cfg)
	_, err := p.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

// An HTML proxy error used to be decoded as JSON, so the caller saw a parse
// error instead of the HTTP status.
func TestEmbeddingProviderNonJSONErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	p := NewEmbedding(&config.EmbeddingProviderConfig{
		BaseURL:   srv.URL,
		Model:     "test-model",
		Dimension: 4,
	})
	_, err := p.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected an error for a 502 response")
	}
	msg := err.Error()
	if !strings.Contains(msg, "502") {
		t.Errorf("error should name the HTTP status, got: %v", err)
	}
	if strings.Contains(msg, "decoding") || strings.Contains(msg, "invalid character") {
		t.Errorf("error should not be a JSON parse error, got: %v", err)
	}
	if !strings.Contains(msg, "Bad Gateway") {
		t.Errorf("error should include a body excerpt, got: %v", err)
	}
}

// The indexer retries only what the provider marks transient, so the
// classification of each status is part of the contract.
func TestEmbeddingProviderClassifiesTransientStatuses(t *testing.T) {
	for _, tc := range []struct {
		status    int
		transient bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
		}))
		p := NewEmbedding(&config.EmbeddingProviderConfig{BaseURL: srv.URL, Model: "m", Dimension: 4})
		_, err := p.Embed(context.Background(), []string{"hello"})
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", tc.status)
		}
		if got := errors.Is(err, ErrTransient); got != tc.transient {
			t.Errorf("status %d: transient=%v, want %v (%v)", tc.status, got, tc.transient, err)
		}
	}
}

// A cancelled request is the caller's decision, not a flaky endpoint.
func TestEmbeddingProviderCancellationIsNotTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewEmbedding(&config.EmbeddingProviderConfig{BaseURL: srv.URL, Model: "m", Dimension: 4})
	_, err := p.Embed(ctx, []string{"hello"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancellation to surface, got %v", err)
	}
	if errors.Is(err, ErrTransient) {
		t.Errorf("a cancelled context must not be retryable, got %v", err)
	}
}

// A body that stops mid-flight is as retryable as a request that never
// connected — otherwise the indexer skips the batch instead of retrying it.
func TestEmbeddingProviderTruncatedBodyIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		// Promise more body than we send, then hang up.
		_, _ = conn.Write([]byte("HTTP/1.1 503 Service Unavailable\r\nContent-Length: 500\r\n\r\npartial"))
		conn.Close()
	}))
	defer srv.Close()

	p := NewEmbedding(&config.EmbeddingProviderConfig{BaseURL: srv.URL, Model: "m", Dimension: 4})
	_, err := p.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected an error for a truncated response body")
	}
	if !errors.Is(err, ErrTransient) {
		t.Errorf("a truncated body must be retryable, got %v", err)
	}
}
