// SPDX-License-Identifier: GPL-3.0-or-later

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout is the per-request timeout applied by New() when
// the caller does not supply WithTimeout.
const DefaultTimeout = 60 * time.Second

// Retry behaviour. We retry on 429 (rate limit) and 5xx (server
// error) only; 4xx other than 429 is treated as a permanent client
// error and not retried (e.g. 401 = bad API key, 400 = bad model).
const (
	maxRetries     = 3
	baseBackoff    = 200 * time.Millisecond
	maxBackoff     = 5 * time.Second
	backoffJitter  = 100 * time.Millisecond
)

// userAgent is sent with every request.
const userAgent = "readeckorator/dev (https://github.com/inful/readeckorator)"

// Client posts chat-completion requests to an OpenAI-compatible
// endpoint. Safe for concurrent use.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// New constructs a Client. baseURL should be the root of the
// OpenAI-compatible API (e.g. "https://api.openai.com/v1" or
// "http://localhost:11434/v1"); the Client appends "/chat/completions".
func New(baseURL, apiKey string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithHTTPClient swaps the underlying *http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithTimeout overrides DefaultTimeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.httpClient.Timeout = d }
}

// Chat posts a chat-completion request and returns the assistant
// message content. Always requests JSON-mode output via
// response_format: {type:"json_object"}; callers parse the content
// as JSON.
//
// Retries up to maxRetries times on 429 and 5xx with exponential
// backoff. Honours ctx cancellation at every wait.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Always force JSON-mode regardless of what the caller set.
	// The classifier pipeline depends on parseable JSON output.
	req.ResponseFormat = JSONMode

	body, err := json.Marshal(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return ChatResponse{}, err
		}
		if attempt > 0 {
			if err := sleepWithBackoff(ctx, attempt); err != nil {
				return ChatResponse{}, err
			}
		}

		resp, err := c.doOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		if !shouldRetry(err) {
			return ChatResponse{}, err
		}
		lastErr = err
	}
	return ChatResponse{}, fmt.Errorf("llm: gave up after %d retries: %w", maxRetries, lastErr)
}

// doOnce issues a single HTTP request and decodes the response.
// The returned error wraps the HTTP status code so shouldRetry can
// inspect it.
func (c *Client) doOnce(ctx context.Context, body []byte) (ChatResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx,
		http.MethodPost,
		c.baseURL+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("llm request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		// Read a snippet for diagnostics; the body will be
		// discarded when the connection is reused.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ChatResponse{}, retryableError{
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(snippet)),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ChatResponse{}, fmt.Errorf("llm HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ChatResponse{}, fmt.Errorf("decode llm response: %w", err)
	}

	if len(out.Choices) == 0 {
		return ChatResponse{}, errors.New("llm response: choices array is empty")
	}
	if out.Choices[0].Message.Content == "" {
		return ChatResponse{}, errors.New("llm response: empty content in first choice")
	}
	return out, nil
}

// retryableError lets shouldRetry distinguish between errors that
// warrant a retry and those that don't, without leaking HTTP
// internals to the caller.
type retryableError struct {
	status int
	body   string
}

func (e retryableError) Error() string {
	return fmt.Sprintf("llm HTTP %d: %s", e.status, e.body)
}

// shouldRetry reports whether err represents a transient
// condition (429, 5xx).
func shouldRetry(err error) bool {
	var re retryableError
	return errors.As(err, &re)
}

// sleepWithBackoff sleeps for an exponentially-growing duration
// (capped at maxBackoff) plus a small random jitter, while staying
// inside the context's deadline.
func sleepWithBackoff(ctx context.Context, attempt int) error {
	delay := baseBackoff * (1 << (attempt - 1))
	if delay > maxBackoff {
		delay = maxBackoff
	}
	// Math/rand/v2 is fine here — the jitter is for spreading
	// retries, not for security. gosec G404 flags both math/rand
	// and crypto/rand indifferently; we deliberately use the
	// cheaper non-crypto source.
	//nolint:gosec // G404: jitter is not security-sensitive.
	jitter := time.Duration(mathrand.Int64N(int64(backoffJitter)))
	wait := delay + jitter

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// jsonDecode is a tiny helper so tests can decode the request body
// without pulling in a separate decoder.
func jsonDecode(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}
