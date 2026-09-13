// SPDX-License-Identifier: GPL-3.0-or-later

package readeck

import (
	"context"
	"net/http"
)

// ListLabels returns the user's full label inventory.
//
// Readeck returns labels sorted by count descending; we don't
// re-sort so the caller sees the same order as the UI.
func (c *Client) ListLabels(ctx context.Context) ([]Label, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/bookmarks/labels", nil)
	if err != nil {
		return nil, err
	}
	var out []Label
	if err := c.do(req, &out); err != nil {
		return nil, err
	}
	return out, nil
}
