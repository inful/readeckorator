// SPDX-License-Identifier: GPL-3.0-or-later

package readeck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ListBookmarks returns one page of bookmarks matching params. The
// caller is responsible for pagination — use ListBookmarksAll for
// a single-shot "fetch everything matching" call.
func (c *Client) ListBookmarks(ctx context.Context, params BookmarkListParams) ([]Bookmark, error) {
	u := c.baseURL + "/api/bookmarks?" + params.queryValues().Encode()
	req, err := c.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	var out []Bookmark
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListBookmarksAll fetches every page of bookmarks matching params
// and concatenates them into a single slice. Stops when a response
// has no rel="next" Link header.
//
// Order across pages is preserved (page 1 first, then page 2, etc.).
func (c *Client) ListBookmarksAll(ctx context.Context, params BookmarkListParams) ([]Bookmark, error) {
	if params.Limit == 0 {
		params.Limit = 50
	}

	var all []Bookmark
	nextURL := c.baseURL + "/api/bookmarks?" + params.queryValues().Encode()

	for nextURL != "" {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := c.newRequest(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		// We need the Link header in addition to the body, so
		// issue the request manually rather than via do().
		link, page, err := c.fetchPage(ctx, req)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next, err := nextLinkURL(link, c.baseURL)
		if err != nil {
			return nil, fmt.Errorf("parse Link header: %w", err)
		}
		nextURL = next
	}
	return all, nil
}

// fetchPage issues a GET and returns the Link header alongside the
// decoded body. It's split out from do() because do() discards
// headers.
func (c *Client) fetchPage(ctx context.Context, req *http.Request) (link string, items []Bookmark, err error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("do %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("%s %s: HTTP %d",
			req.Method, req.URL.Path, resp.StatusCode)
	}

	if err := decodeJSON(resp.Body, &items); err != nil {
		return "", nil, err
	}
	return resp.Header.Get("Link"), items, nil
}

// GetBookmark returns the full detail for one bookmark by ID.
func (c *Client) GetBookmark(ctx context.Context, id string) (Bookmark, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/bookmarks/"+id, nil)
	if err != nil {
		return Bookmark{}, err
	}
	var out Bookmark
	if err := c.do(req, &out); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Bookmark{}, fmt.Errorf("get bookmark %s: %w", id, ErrNotFound)
		}
		return Bookmark{}, err
	}
	return out, nil
}

// GetArticleMarkdown returns the bookmark's article body as markdown.
// Returns ErrNotFound if the bookmark exists but has no extracted
// article yet.
func (c *Client) GetArticleMarkdown(ctx context.Context, id string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/bookmarks/"+id+"/article.md", nil)
	if err != nil {
		return "", err
	}
	// Override the Accept header — markdown is text/markdown, not
	// JSON. do() will decode JSON if out is non-nil, so we issue
	// the request manually here.
	req.Header.Set("Accept", "text/markdown")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get article markdown %s: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("article markdown %s: %w", id, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("article markdown %s: HTTP %d", id, resp.StatusCode)
	}

	buf, err := readAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read article markdown %s: %w", id, err)
	}
	return buf, nil
}

// UpdateBookmarkLabels applies additive and/or removal label changes
// to one bookmark. Both arguments may be nil/empty.
//
// Readeck's PATCH endpoint accepts add_labels and remove_labels as
// arrays of strings; either or both may be omitted.
func (c *Client) UpdateBookmarkLabels(ctx context.Context, id string, add, remove []string) error {
	body := map[string]any{}
	if len(add) > 0 {
		body["add_labels"] = add
	}
	if len(remove) > 0 {
		body["remove_labels"] = remove
	}

	req, err := c.newRequest(ctx, http.MethodPatch, "/api/bookmarks/"+id, body)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}
