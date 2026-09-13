// SPDX-License-Identifier: GPL-3.0-or-later

package readeck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ListCollections returns every saved-search collection owned by
// the current user. Collections are returned in server order (no
// sort applied here).
func (c *Client) ListCollections(ctx context.Context) ([]Collection, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/bookmarks/collections", nil)
	if err != nil {
		return nil, err
	}
	var out []Collection
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateCollection creates a new collection and returns its
// server-assigned ID. The 201 response body contains the full
// collection info, but callers usually only need the ID.
func (c *Client) CreateCollection(ctx context.Context, params CollectionCreate) (string, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/api/bookmarks/collections", params)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create collection: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("create collection: %w", ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create collection: HTTP %d", resp.StatusCode)
	}

	var created Collection
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("decode create response: %w", err)
	}
	return created.ID, nil
}

// UpdateCollection updates the labels filter (and any other
// supplied fields) on an existing collection. Collections not
// referenced in config are never deleted — only created/updated.
func (c *Client) UpdateCollection(ctx context.Context, id string, params CollectionCreate) error {
	req, err := c.newRequest(ctx, http.MethodPatch, "/api/bookmarks/collections/"+id, params)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// FindCollectionByName returns the first collection whose Name
// exactly matches name, or ErrNotFound if no such collection exists.
//
// We use this to drive the "create if missing, otherwise update"
// reconciliation loop without having to keep a name→ID map in
// memory across calls.
func (c *Client) FindCollectionByName(ctx context.Context, name string) (Collection, error) {
	all, err := c.ListCollections(ctx)
	if err != nil {
		return Collection{}, err
	}
	for _, col := range all {
		if col.Name == name {
			return col, nil
		}
	}
	return Collection{}, fmt.Errorf("collection %q: %w", name, ErrNotFound)
}
