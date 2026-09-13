// SPDX-License-Identifier: GPL-3.0-or-later

package readeck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// userAgent is sent with every request. Readeck logs it on the
// server side, so we set a real value rather than Go's default.
const userAgent = "readeckorator/dev (https://github.com/inful/readeckorator)"

// ErrNotFound is returned when the server replies 404. Callers can
// use errors.Is to test for it without depending on the underlying
// transport.
var ErrNotFound = errors.New("readeck: not found")

// Client is a thin wrapper around net/http that adds Bearer auth,
// JSON encoding/decoding, and pagination helpers for the Readeck
// REST API.
//
// All methods accept a context and are safe for concurrent use.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// New constructs a Client. baseURL may include a path prefix
// (e.g. "https://readeck.example.com/api") and may or may not end
// in a trailing slash — both forms are normalised.
//
// token is the Readeck API token (Profile → API Tokens).
func New(baseURL, token string, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithHTTPClient swaps the underlying *http.Client. Useful in
// tests with httptest.Server.Client() and for callers that need
// custom transports (proxies, mocks).
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithTimeout overrides the default 30s per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.httpClient.Timeout = d }
}

// --- internal helpers --------------------------------------------------

// newRequest builds an *http.Request with the standard Readeck
// headers: Bearer auth, User-Agent, and (when body is non-empty)
// Content-Type: application/json.
//
// path may be either:
//   - a path starting with "/" (joined onto the client's baseURL)
//   - a full URL (used as-is — useful for paginated next-page links)
func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	full := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		full = c.baseURL + path
	}

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		rdr = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(ctx, method, full, rdr)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// do executes the request and decodes the response into out if out
// is non-nil. It returns errors for non-2xx responses, including a
// typed ErrNotFound for 404s.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read up to 4KB of the body for diagnostics — Readeck
		// usually returns {"message": "..."}.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: HTTP %d: %s",
			req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	if out == nil {
		// Drain body so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response from %s %s: %w",
			req.Method, req.URL.Path, err)
	}
	return nil
}

// followNextLink returns the URL for rel="next" from a Link header
// value, or "" if there is no next page.
//
// Readeck uses the standard GitHub-style Link header format:
//   Link: </api/bookmarks?page=2>; rel="next", </api/bookmarks?page=10>; rel="last"
func nextLinkURL(header string, base string) (string, error) {
	if header == "" {
		return "", nil
	}
	for _, part := range strings.Split(header, ",") {
		section := strings.TrimSpace(part)
		pieces := strings.Split(section, ";")
		if len(pieces) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(pieces[0])
		var rel string
		for _, p := range pieces[1:] {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, `rel="`) && strings.HasSuffix(p, `"`) {
				rel = p[5 : len(p)-1]
			}
		}
		if rel != "next" {
			continue
		}
		// urlPart is like "<https://readeck/api/bookmarks?page=2>".
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		raw := urlPart[1 : len(urlPart)-1]
		// Resolve relative URLs against the base.
		if base != "" {
			baseU, err := url.Parse(base)
			if err != nil {
				return "", err
			}
			ref, err := url.Parse(raw)
			if err != nil {
				return "", err
			}
			return baseU.ResolveReference(ref).String(), nil
		}
		return raw, nil
	}
	return "", nil
}
