// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the collections reconciler.
//
// The reconciler syncs the configured CollectionGroup list with
// the user's Readeck instance. Rules:
//   - group with no matching collection → POST to create
//   - group with matching name but wrong labels → PATCH
//   - group matching by name AND labels → no-op
//   - existing collections not in config are NOT deleted
//
// We use an httptest.Server with a custom handler that maintains
// a tiny in-memory store keyed by collection name, so we can
// assert on creation order, update payloads, and idempotency.

package collections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/inful/readeckorator/internal/readeck"
)

// memStore is a tiny in-memory replacement for the Readeck
// collection store, sufficient to drive the reconciler through
// every interesting path.
type memStore struct {
	mu   sync.Mutex
	rows map[string]readeck.Collection
}

func newMemStore() *memStore {
	return &memStore{rows: make(map[string]readeck.Collection)}
}

// serveHTTP routes requests for the memStore. Lives as a method
// (not a closure) so we can use tagged switches without QF1002
// noise.
func (m *memStore) serveHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/bookmarks/collections":
		out := make([]readeck.Collection, 0, len(m.rows))
		for _, c := range m.rows {
			out = append(out, c)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)

	case r.Method == http.MethodPost && r.URL.Path == "/api/bookmarks/collections":
		var in readeck.CollectionCreate
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if in.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if _, exists := m.rows[in.Name]; exists {
			http.Error(w, "already exists", http.StatusConflict)
			return
		}
		m.rows[in.Name] = readeck.Collection{
			ID:     nextID(),
			Name:   in.Name,
			Labels: in.Labels,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(m.rows[in.Name])

	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/bookmarks/collections/"):
		body, _ := io.ReadAll(r.Body)
		id := strings.TrimPrefix(r.URL.Path, "/api/bookmarks/collections/")
		var existing readeck.Collection
		found := false
		for _, c := range m.rows {
			if c.ID == id {
				existing = c
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var in readeck.CollectionCreate
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		existing.Labels = in.Labels
		if in.Name != "" && in.Name != existing.Name {
			delete(m.rows, existing.Name)
			existing.Name = in.Name
		}
		m.rows[existing.Name] = existing
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "no route", http.StatusNotFound)
	}
}

func (m *memStore) handler() http.Handler {
	return http.HandlerFunc(m.serveHTTP)
}

var idCounter int64

func nextID() string {
	n := atomic.AddInt64(&idCounter, 1)
	return fmt.Sprintf("id-%d", n)
}

// newReconciler spins up an httptest server with the memStore,
// creates a readeck.Client pointing at it, and returns a
// Reconciler wired up to use that client.
func newReconciler(t *testing.T) (*Reconciler, *memStore) {
	t.Helper()
	store := newMemStore()
	srv := httptest.NewServer(store.handler())
	t.Cleanup(srv.Close)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	return New(rc), store
}

// --- Create-if-missing --------------------------------------------------

func TestReconcile_CreatesMissingCollections(t *testing.T) {
	r, store := newReconciler(t)

	groups := []Group{
		{Name: "Tech", Labels: []string{"technology", "programming"}},
		{Name: "Food", Labels: []string{"cooking"}},
	}

	if err := r.Reconcile(context.Background(), groups); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := len(store.rows); got != 2 {
		t.Fatalf("rows: got %d, want 2", got)
	}
	tech := store.rows["Tech"]
	if tech.ID == "" {
		t.Errorf("Tech collection missing ID")
	}
	// Reconciler sorts labels for deterministic output.
	if tech.Labels != "programming,technology" {
		t.Errorf("Tech.Labels: got %q, want %q", tech.Labels, "programming,technology")
	}
}

func TestReconcile_EmptyGroupsIsNoOp(t *testing.T) {
	r, store := newReconciler(t)
	if err := r.Reconcile(context.Background(), nil); err != nil {
		t.Fatalf("Reconcile (nil): %v", err)
	}
	if err := r.Reconcile(context.Background(), []Group{}); err != nil {
		t.Fatalf("Reconcile (empty): %v", err)
	}
	if got := len(store.rows); got != 0 {
		t.Errorf("rows: got %d, want 0", got)
	}
}

// --- Update-if-exists ---------------------------------------------------

func TestReconcile_UpdatesExistingCollection(t *testing.T) {
	r, store := newReconciler(t)

	store.rows["Tech"] = readeck.Collection{
		ID:     "id-1",
		Name:   "Tech",
		Labels: "old,stale",
	}

	groups := []Group{
		{Name: "Tech", Labels: []string{"technology", "programming"}},
	}

	if err := r.Reconcile(context.Background(), groups); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := store.rows["Tech"]
	if got.Labels != "programming,technology" {
		t.Errorf("Tech.Labels: got %q, want %q", got.Labels, "programming,technology")
	}
}

func TestReconcile_ExistingMatchIsNoOp(t *testing.T) {
	r, store := newReconciler(t)
	store.rows["Tech"] = readeck.Collection{
		ID:     "id-1",
		Name:   "Tech",
		Labels: "programming,technology",
	}
	if err := r.Reconcile(context.Background(), []Group{
		{Name: "Tech", Labels: []string{"technology", "programming"}},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := store.rows["Tech"]
	if got.Labels != "programming,technology" {
		t.Errorf("Tech.Labels changed unexpectedly: got %q", got.Labels)
	}
}

// --- Mixed pass ---------------------------------------------------------

func TestReconcile_CreatesAndUpdatesInOnePass(t *testing.T) {
	r, store := newReconciler(t)

	store.rows["Tech"] = readeck.Collection{
		ID:     "id-1",
		Name:   "Tech",
		Labels: "old",
	}

	groups := []Group{
		{Name: "Tech", Labels: []string{"technology"}},
		{Name: "Food", Labels: []string{"cooking"}},
		{Name: "News", Labels: []string{"news"}},
	}

	if err := r.Reconcile(context.Background(), groups); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(store.rows) != 3 {
		t.Errorf("rows: got %d, want 3", len(store.rows))
	}
	if store.rows["Tech"].Labels != "technology" {
		t.Errorf("Tech.Labels: got %q, want %q", store.rows["Tech"].Labels, "technology")
	}
	if _, ok := store.rows["Food"]; !ok {
		t.Errorf("Food not created")
	}
	if _, ok := store.rows["News"]; !ok {
		t.Errorf("News not created")
	}
}

// --- Does not delete ---------------------------------------------------

func TestReconcile_DoesNotDeleteUnrelatedCollections(t *testing.T) {
	r, store := newReconciler(t)

	store.rows["PreExisting"] = readeck.Collection{
		ID:   "id-1",
		Name: "PreExisting",
	}

	groups := []Group{
		{Name: "Tech", Labels: []string{"technology"}},
	}

	if err := r.Reconcile(context.Background(), groups); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if _, ok := store.rows["PreExisting"]; !ok {
		t.Errorf("PreExisting was deleted (should have been preserved)")
	}
	if _, ok := store.rows["Tech"]; !ok {
		t.Errorf("Tech was not created")
	}
}

// --- Idempotency -------------------------------------------------------

func TestReconcile_IsIdempotent(t *testing.T) {
	r, store := newReconciler(t)
	groups := []Group{
		{Name: "Tech", Labels: []string{"technology"}},
		{Name: "Food", Labels: []string{"cooking"}},
	}

	for i := 0; i < 3; i++ {
		if err := r.Reconcile(context.Background(), groups); err != nil {
			t.Fatalf("Reconcile #%d: %v", i, err)
		}
	}

	if len(store.rows) != 2 {
		t.Errorf("rows after 3 runs: got %d, want 2", len(store.rows))
	}
}

// --- Labels filter formatting -------------------------------------------

func TestReconcile_FormatsLabelsAsCommaJoined(t *testing.T) {
	r, store := newReconciler(t)
	groups := []Group{
		{Name: "Tech", Labels: []string{"a", "b", "c", "d"}},
	}
	if err := r.Reconcile(context.Background(), groups); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := store.rows["Tech"].Labels
	want := "a,b,c,d"
	if got != want {
		t.Errorf("Labels: got %q, want %q", got, want)
	}
}

func TestReconcile_SortsLabelsBeforeSending(t *testing.T) {
	r, store := newReconciler(t)
	groups := []Group{
		{Name: "Tech", Labels: []string{"zeta", "alpha", "mike"}},
	}
	if err := r.Reconcile(context.Background(), groups); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := store.rows["Tech"].Labels
	want := "alpha,mike,zeta"
	if got != want {
		t.Errorf("Labels: got %q, want %q", got, want)
	}
}

// --- Error propagation --------------------------------------------------

func TestReconcile_CreateErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case http.MethodPost:
			http.Error(w, "denied", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	r := New(rc)

	err := r.Reconcile(context.Background(), []Group{
		{Name: "Tech", Labels: []string{"x"}},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestReconcile_UpdateErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":"id-1","name":"Tech","labels":"old"}]`))
		case http.MethodPatch:
			http.Error(w, "denied", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	r := New(rc)

	err := r.Reconcile(context.Background(), []Group{
		{Name: "Tech", Labels: []string{"new"}},
	})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
