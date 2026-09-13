// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the OpenAI-compatible chat completions client.
//
// The LLM client posts JSON to {base_url}/chat/completions and
// decodes the response. These tests stand up an httptest.Server
// that mimics the OpenAI / Ollama / vLLM / LM Studio shape:
//
//   POST /v1/chat/completions
//   200 OK
//   {
//     "choices": [{"message": {"role":"assistant","content":"..."}}],
//     "usage": {"prompt_tokens":N,"completion_tokens":M},
//     "model": "..."
//   }
//
// We also exercise the retry path (429, 5xx) by having the test
// server fail N times before returning success.

package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestServer spins up an httptest.Server with the given handler
// and returns a Client wired to it. The client base URL is the
// server's URL plus the standard OpenAI-compatible /v1 suffix
// because the LLM client appends "/chat/completions" to baseURL.
func newTestServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/v1", "test-key", WithHTTPClient(srv.Client()))
}

// --- Client construction -------------------------------------------------

func TestNew_StripsTrailingSlash(t *testing.T) {
	c := New("https://api.example.com/v1/", "k")
	if c.baseURL != "https://api.example.com/v1" {
		t.Errorf("baseURL: got %q, want trailing slash stripped", c.baseURL)
	}
}

func TestNew_DefaultTimeout(t *testing.T) {
	c := New("https://api.example.com/v1", "k")
	if c.httpClient.Timeout != DefaultTimeout {
		t.Errorf("Timeout: got %v, want %v", c.httpClient.Timeout, DefaultTimeout)
	}
}

// --- Chat completion happy path -----------------------------------------

func TestChat_SendsCorrectRequestShape(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path: got %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("Authorization: got %q, want %q", auth, "Bearer test-key")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "x",
			"model": "test-model",
			"choices": [{"index":0,"message":{"role":"assistant","content":"hello"}}]
		}`))
	})

	resp, err := c.Chat(context.Background(), ChatRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: "system", Content: "you classify"},
			{Role: "user", Content: "describe"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content() != "hello" {
		t.Errorf("Content: got %q, want %q", resp.Content(), "hello")
	}
	if resp.Model != "test-model" {
		t.Errorf("Model: got %q", resp.Model)
	}
}

func TestChat_PassesThroughTemperature(t *testing.T) {
	var gotTemp float64
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		gotTemp = body["temperature"].(float64)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	})

	if _, err := c.Chat(context.Background(), ChatRequest{
		Model:       "m",
		Temperature: 0.42,
		Messages:    []Message{{Role: "user", Content: "x"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotTemp != 0.42 {
		t.Errorf("temperature: got %v, want 0.42", gotTemp)
	}
}

func TestChat_AlwaysRequestsJSONMode(t *testing.T) {
	var gotRF map[string]any
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if rf, ok := body["response_format"].(map[string]any); ok {
			gotRF = rf
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"}}]}`))
	})

	_, _ = c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if gotRF["type"] != "json_object" {
		t.Errorf("response_format.type: got %v, want json_object", gotRF["type"])
	}
}

// --- Error paths --------------------------------------------------------

func TestChat_400ReturnsError(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model"}}`))
	})
	_, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestChat_RetriesOn429ThenSucceeds(t *testing.T) {
	var attempts int32
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	})

	resp, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content() != "ok" {
		t.Errorf("Content: got %q, want %q", resp.Content(), "ok")
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts: got %d, want 3 (2 retries + 1 success)", got)
	}
}

func TestChat_RetriesOn500ThenSucceeds(t *testing.T) {
	var attempts int32
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`boom`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"}}]}`))
	})

	resp, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content() != "recovered" {
		t.Errorf("Content: got %q", resp.Content())
	}
}

func TestChat_GivesUpAfterMaxRetries(t *testing.T) {
	var attempts int32
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`down`))
	})

	_, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error after max retries, got nil")
	}
	if got := atomic.LoadInt32(&attempts); got != int32(maxRetries)+1 {
		t.Errorf("attempts: got %d, want %d", got, maxRetries+1)
	}
}

func TestChat_DoesNotRetryOn401(t *testing.T) {
	var attempts int32
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
	})

	_, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts: got %d, want 1 (no retry on 401)", got)
	}
}

func TestChat_RespectsContextCancellation(t *testing.T) {
	// Server that blocks indefinitely. The client's 50ms context
	// should cause Chat to return with context.DeadlineExceeded.
	// The handler's r.Context().Done() is cancelled when the
	// client closes the connection, letting the server exit
	// cleanly so srv.Close doesn't block.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.CloseClientConnections) //nolint:staticcheck // intentional

	c := New(srv.URL+"/v1", "k", WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.Chat(ctx, ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded, got: %v", err)
	}
}

// --- Empty / malformed responses ----------------------------------------

func TestChat_EmptyChoicesReturnsError(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	_, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error for empty choices, got nil")
	}
	if !strings.Contains(err.Error(), "choices") {
		t.Errorf("error should mention choices, got: %v", err)
	}
}

func TestChat_MalformedJSONReturnsError(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{ not json`))
	})
	_, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error for malformed JSON, got nil")
	}
}

func TestChat_ContextErrIsPropagated(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Chat(ctx, ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap context.Canceled, got: %v", err)
	}
}

// --- helpers ------------------------------------------------------------

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := jsonDecode(r.Body, &m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}
