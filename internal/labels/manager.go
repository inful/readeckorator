// SPDX-License-Identifier: GPL-3.0-or-later

// Package labels is the bridge between the LLM's classification
// output and the Readeck API call that adds labels to a bookmark.
//
// Manager is the only entry point. It owns three responsibilities:
//
//   - Filter: pure function that takes a raw label list from the
//     LLM and applies normalisation + inventory policy.
//
//   - Apply: pushes the filtered labels to Readeck (ADDITIVE
//     ONLY — we never tell Readeck to remove labels) and records
//     the outcome in the state DB so the inventory stays
//     current and the bookmark isn't re-classified next pass.
//
//   - Inventory: returns a sorted view of known labels, used to
//     build the system prompt's "existing labels" hint.
//
// Manager is goroutine-safe; the underlying *state.Store and
// *readeck.Client are both safe for concurrent use.
package labels

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/inful/readeckorator/internal/readeck"
	"github.com/inful/readeckorator/internal/state"
)

// Manager owns label-related side effects.
type Manager struct {
	store     *state.Store
	client    *readeck.Client
	inventory []string // optional pre-loaded inventory; loaded by Inventory() otherwise
}

// New constructs a Manager. store is required; client may be nil
// for tests that exercise Filter() only (which doesn't touch
// Readeck).
func New(store *state.Store, client *readeck.Client) *Manager {
	return &Manager{store: store, client: client}
}

// Filter normalises the proposed label list and, when allowNew is
// false, drops any label that isn't already in the inventory.
// Returns an empty slice (never nil) when nothing survives.
//
// Output order is the input order with duplicates removed — this
// lets the caller preserve the LLM's ordering for display.
//
// Labels are normalised: trimmed, lower-cased, and deduped. When
// allowNew is false, we match case-insensitively against the
// inventory and emit the canonical (inventory) form, so "TECH"
// with an inventory entry "Tech" becomes "Tech".
func (m *Manager) Filter(proposed []string, allowNew bool) []string {
	if len(proposed) == 0 {
		return []string{}
	}

	// Build a case-insensitive lookup of inventory for both
	// filtering (allowNew=false) and canonicalisation.
	inventoryByLower := make(map[string]string, len(m.inventory))
	for _, inv := range m.inventory {
		inventoryByLower[strings.ToLower(inv)] = inv
	}

	seen := make(map[string]bool, len(proposed))
	out := make([]string, 0, len(proposed))
	for _, raw := range proposed {
		n := strings.ToLower(strings.TrimSpace(raw))
		if n == "" {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		if !allowNew {
			canonical, ok := inventoryByLower[n]
			if !ok {
				continue // drop unknown when policy forbids new
			}
			n = canonical // use inventory's casing
		}
		out = append(out, n)
	}
	if out == nil {
		return []string{}
	}
	return out
}

// Apply pushes labels to Readeck additively and records the
// outcome in the state DB. The two writes are not transactional;
// if the state DB write fails after Readeck succeeds, the next
// pass will see the labels in Readeck but treat the bookmark as
// unprocessed and try again — which is fine for an additive-only
// classifier (idempotent apply).
//
// An empty labels slice is a successful "LLM said no labels" — we
// still mark the bookmark processed so the next pass skips it
// rather than re-prompting the LLM. The Readeck call is skipped
// (there's nothing to add) but the state DB write happens.
func (m *Manager) Apply(ctx context.Context, bookmarkID string, labels []string, model string, confidence float64) error {
	normalised := normaliseForApply(labels)

	if m.client != nil && len(normalised) > 0 {
		if err := m.client.UpdateBookmarkLabels(ctx, bookmarkID, normalised, nil); err != nil {
			return fmt.Errorf("update bookmark labels: %w", err)
		}
	}

	// Always record the bookmark, even with empty labels — see
	// comment above.
	if err := m.store.MarkProcessed(ctx, bookmarkID, normalised, model, confidence); err != nil {
		return fmt.Errorf("mark processed: %w", err)
	}
	for _, label := range normalised {
		if err := m.store.RecordLabel(ctx, label); err != nil {
			return fmt.Errorf("record label %q: %w", label, err)
		}
	}
	return nil
}

// Inventory returns the labels known to the system, sorted
// alphabetically. Sources, in order:
//
//  1. The state DB's label_inventory table (every label the LLM
//     has produced so far).
//  2. Readeck's live /api/bookmarks/labels (catches labels that
//     exist on Readeck but were never produced by this app).
//
// Duplicates are merged; case-insensitive equality. The result is
// always sorted alphabetically so the system prompt is stable.
func (m *Manager) Inventory(ctx context.Context) ([]string, error) {
	seen := make(map[string]bool)
	merged := []string{}

	// Source 1: state DB inventory.
	stats, err := m.store.ListLabels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list labels from state: %w", err)
	}
	for _, s := range stats {
		key := strings.ToLower(s.Name)
		if !seen[key] {
			seen[key] = true
			merged = append(merged, s.Name)
		}
	}

	// Source 2: live Readeck inventory.
	if m.client != nil {
		labels, err := m.client.ListLabels(ctx)
		if err != nil {
			// Don't fail the whole operation if Readeck is
			// briefly unreachable — the state DB inventory is
			// good enough on its own.
			//nolint:nilerr // intentional soft-fail on Readeck
			return sortedUnique(merged), nil
		}
		for _, l := range labels {
			key := strings.ToLower(l.Name)
			if !seen[key] {
				seen[key] = true
				merged = append(merged, l.Name)
			}
		}
	}

	return sortedUnique(merged), nil
}

// WithInventory lets the caller seed the manager's in-memory
// inventory list. Tests use this to simulate "we already know
// about these labels" without round-tripping to the DB.
func (m *Manager) WithInventory(inv []string) *Manager {
	m.inventory = append([]string(nil), inv...)
	return m
}

// normaliseForApply does what Filter does, but always allows new
// labels — the policy check belongs in Filter (which the caller
// invokes before Apply), not in Apply itself.
func normaliseForApply(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		n := strings.ToLower(strings.TrimSpace(raw))
		if n == "" {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// sortedUnique returns a sorted copy of the input with duplicates
// (case-insensitive) removed.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
