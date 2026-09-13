// SPDX-License-Identifier: GPL-3.0-or-later

// Package collections reconciles the configured label groups with
// the user's Readeck collections.
//
// The reconciler is intentionally non-destructive: collections that
// aren't in the config are left untouched. Users may have other
// tools (or hand-curated collections) that we shouldn't delete
// just because we don't know about them.
//
// One Reconciler pass:
//   - Lists every existing collection from Readeck.
//   - For each configured Group:
//       * If a collection with that Name exists AND its labels
//         filter already matches → no-op.
//       * If it exists with different labels → PATCH it.
//       * If it doesn't exist → POST to create it.
package collections

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/inful/readeckorator/internal/readeck"
)

// Group is one configured label-to-collection mapping.
//
// Mirrors config.CollectionGroup but lives here so the collections
// package doesn't need to depend on the config package.
type Group struct {
	Name   string
	Labels []string
}

// Reconciler owns the read+diff+write loop against the Readeck
// collections API.
type Reconciler struct {
	client *readeck.Client
}

// New constructs a Reconciler.
func New(client *readeck.Client) *Reconciler {
	return &Reconciler{client: client}
}

// Reconcile brings the user's Readeck collections in line with
// the configured groups. Returns the first error encountered;
// partial progress is left in place (a later run with a fixed
// config will resume).
func (r *Reconciler) Reconcile(ctx context.Context, groups []Group) error {
	if len(groups) == 0 {
		return nil
	}

	existing, err := r.client.ListCollections(ctx)
	if err != nil {
		return fmt.Errorf("list collections: %w", err)
	}

	byName := make(map[string]readeck.Collection, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}

	for _, g := range groups {
		want := joinLabels(g.Labels)
		current, ok := byName[g.Name]
		if !ok {
			// Create.
			id, err := r.client.CreateCollection(ctx, readeck.CollectionCreate{
				Name:   g.Name,
				Labels: want,
			})
			if err != nil {
				return fmt.Errorf("create collection %q: %w", g.Name, err)
			}
			_ = id // We don't currently store the ID; the next
			// reconciliation looks up by name.
			continue
		}
		if current.Labels == want {
			// Already in sync — nothing to do.
			continue
		}
		// Update.
		if err := r.client.UpdateCollection(ctx, current.ID, readeck.CollectionCreate{
			Name:   g.Name,
			Labels: want,
		}); err != nil {
			return fmt.Errorf("update collection %q: %w", g.Name, err)
		}
	}
	return nil
}

// joinLabels renders the labels as a comma-joined string (the
// format Readeck expects for the labels filter). Labels are
// sorted for determinism — the reconciler should not produce a
// different string on subsequent runs for the same input.
func joinLabels(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}
