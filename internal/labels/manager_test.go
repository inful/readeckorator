// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the labels manager.
//
// The manager is the bridge between the LLM's classification output
// and the Readeck API call that adds labels to a bookmark. Its
// responsibilities are:
//   - Filter: take a raw label list and apply normalisation +
//     inventory policy (drop unknowns when allowNew=false)
//   - Apply: push the filtered labels to Readeck (additive only)
//     and record the outcome in the state DB so subsequent
//     passes can use the new inventory
//   - Inventory: hand back a sorted view of known labels for
//     prompt-building
//
// Tests cover each surface independently; we don't reach for the
// full Readeck API in pure-filter tests, and we don't reach for
// the state DB in API tests.

package labels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inful/readeckorator/internal/readeck"
	"github.com/inful/readeckorator/internal/state"
)

// newTestStore returns a fresh state.Store backed by t.TempDir().
func newTestStore(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := state.Open(dir + "/state.db")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// readeckStub captures the last PATCH request and serves a canned
// response. Tests assert against the captured request.
type readeckStub struct {
	patches    int32
	lastBody   string
	lastPath   string
	respondErr bool
}

func (s *readeckStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/bookmarks/"):
			buf := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buf)
			s.lastBody = string(buf)
			s.lastPath = r.URL.Path
			atomic.AddInt32(&s.patches, 1)
			if s.respondErr {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/api/bookmarks/labels"):
			_, _ = w.Write([]byte(`[
				{"name":"tech","count":3},
				{"name":"news","count":2},
				{"name":"cooking","count":1}
			]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// --- Filter ------------------------------------------------------------

func TestFilter_EmptyInputReturnsEmpty(t *testing.T) {
	m := &Manager{}
	got := m.Filter(nil, true)
	if got == nil {
		t.Errorf("Filter: got nil, want empty slice")
	}
	if len(got) != 0 {
		t.Errorf("Filter: got %v, want []", got)
	}
}

func TestFilter_AllowsAllWhenAllowNewTrue(t *testing.T) {
	m := &Manager{}
	got := m.Filter([]string{"tech", "cooking", "Made-Up"}, true)
	want := []string{"tech", "cooking", "made-up"} // normalised: trim+lower
	if !equalStringSlices(got, want) {
		t.Errorf("Filter: got %v, want %v", got, want)
	}
}

func TestFilter_DropsUnknownsWhenAllowNewFalse(t *testing.T) {
	m := &Manager{
		inventory: []string{"tech", "cooking", "news"},
	}
	got := m.Filter([]string{"tech", "Made-Up", "cooking"}, false)
	want := []string{"tech", "cooking"}
	if !equalStringSlices(got, want) {
		t.Errorf("Filter: got %v, want %v", got, want)
	}
}

func TestFilter_NormalisesCaseAndWhitespace(t *testing.T) {
	m := &Manager{}
	got := m.Filter([]string{"Tech", " tech ", "TECH", "ai"}, true)
	want := []string{"tech", "ai"}
	if !equalStringSlices(got, want) {
		t.Errorf("Filter: got %v, want %v", got, want)
	}
}

func TestFilter_DedupesAcrossInputs(t *testing.T) {
	m := &Manager{}
	got := m.Filter([]string{"tech", "Tech", "TECH"}, true)
	want := []string{"tech"}
	if !equalStringSlices(got, want) {
		t.Errorf("Filter: got %v, want %v", got, want)
	}
}

func TestFilter_MatchesExistingCaseInsensitively(t *testing.T) {
	m := &Manager{
		inventory: []string{"Technology", "Programming"},
	}
	// Input is lowercase; inventory is Titlecase. Filter should
	// recognise "technology" matches "Technology" and keep the
	// canonical form from the inventory.
	got := m.Filter([]string{"technology", "programming", "made-up"}, false)
	want := []string{"Technology", "Programming"}
	if !equalStringSlices(got, want) {
		t.Errorf("Filter: got %v, want %v", got, want)
	}
}

func TestFilter_DropsEmpties(t *testing.T) {
	m := &Manager{}
	got := m.Filter([]string{"", "  ", "\t", "tech"}, true)
	want := []string{"tech"}
	if !equalStringSlices(got, want) {
		t.Errorf("Filter: got %v, want %v", got, want)
	}
}

// --- Apply -------------------------------------------------------------

func TestApply_AddsLabelsToReadeck(t *testing.T) {
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	err := m.Apply(context.Background(), "bm1", []string{"tech", "cooking"}, "model-x", 0.9)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := atomic.LoadInt32(&stub.patches); got != 1 {
		t.Errorf("patches: got %d, want 1", got)
	}
	if !strings.Contains(stub.lastBody, `"add_labels":["tech","cooking"]`) {
		t.Errorf("body should contain add_labels, got %s", stub.lastBody)
	}
}

func TestApply_OnlySendsAddLabels(t *testing.T) {
	// We are strictly additive. The body must never include
	// remove_labels.
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	err := m.Apply(context.Background(), "bm1", []string{"tech"}, "model-x", 0.9)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(stub.lastBody, "remove_labels") {
		t.Errorf("body must never contain remove_labels, got %s", stub.lastBody)
	}
}

func TestApply_EmptyLabelsIsNoOp(t *testing.T) {
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	err := m.Apply(context.Background(), "bm1", nil, "model-x", 0.9)
	if err != nil {
		t.Fatalf("Apply (nil labels): %v", err)
	}
	err = m.Apply(context.Background(), "bm2", []string{}, "model-x", 0.9)
	if err != nil {
		t.Fatalf("Apply (empty labels): %v", err)
	}
	if got := atomic.LoadInt32(&stub.patches); got != 0 {
		t.Errorf("patches: got %d, want 0 (no API call for empty labels)", got)
	}
}

// TestApply_DryRunWritesNothing is the regression test for the
// user-reported bug: --dry-run was writing to the state DB even
// though it's supposed to be a pure preview. This polluted the
// "what's new?" filter and caused the next non-dry-run to skip
// already-classified bookmarks.
func TestApply_DryRunWritesNothing(t *testing.T) {
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc).WithDryRun(true)

	err := m.Apply(context.Background(), "bm1", []string{"tech"}, "model-x", 0.9)
	if err != nil {
		t.Fatalf("Apply in dry-run mode: %v", err)
	}

	// No PATCH should have been sent to Readeck.
	if got := atomic.LoadInt32(&stub.patches); got != 0 {
		t.Errorf("dry-run PATCH count: got %d, want 0", got)
	}

	// No state DB write either.
	processed, err := store.IsProcessed(context.Background(), "bm1")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	if processed {
		t.Errorf("dry-run marked bookmark processed: state DB was written when it shouldn't be")
	}

	stats, err := store.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("dry-run label inventory: got %v, want empty", stats)
	}
}

func TestApply_DryRunCanBeToggledPerCall(t *testing.T) {
	// Calling WithDryRun(true) then later calling Apply with the
	// dry-run flag turned off should write normally. (The CLI flag
	// is set once per process, but the setter is the right shape
	// if a future caller wants per-bookmark overrides.)
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))

	m := New(store, rc)
	m.WithDryRun(true)

	if err := m.Apply(context.Background(), "bm1", []string{"tech"}, "model-x", 0.9); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if processed, _ := store.IsProcessed(context.Background(), "bm1"); processed {
		t.Errorf("dry-run marked bm1 processed")
	}

	m.WithDryRun(false)
	if err := m.Apply(context.Background(), "bm1", []string{"tech"}, "model-y", 0.9); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if processed, _ := store.IsProcessed(context.Background(), "bm1"); !processed {
		t.Errorf("real Apply should have marked bm1 processed")
	}
	if got := atomic.LoadInt32(&stub.patches); got != 1 {
		t.Errorf("real Apply PATCH count: got %d, want 1", got)
	}
}

func TestApply_RecordsInStateDB(t *testing.T) {
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	if err := m.Apply(context.Background(), "bm1", []string{"tech", "ai"}, "model-x", 0.92); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The state DB should record this bookmark as processed.
	got, err := store.IsProcessed(context.Background(), "bm1")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	if !got {
		t.Errorf("IsProcessed after Apply: got false, want true")
	}

	// And the labels should appear in the inventory.
	stats, err := store.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	names := make([]string, 0, len(stats))
	for _, s := range stats {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	want := []string{"ai", "tech"}
	if !equalStringSlices(names, want) {
		t.Errorf("ListLabels: got %v, want %v", names, want)
	}
}

func TestApply_ReadeckFailureSurfacesError(t *testing.T) {
	stub := &readeckStub{respondErr: true}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	err := m.Apply(context.Background(), "bm1", []string{"tech"}, "model-x", 0.9)
	if err == nil {
		t.Fatalf("expected error from Readeck failure")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}

	// On failure, state should NOT have recorded the bookmark
	// (otherwise we'd skip it next pass even though no labels
	// were applied).
	processed, err := store.IsProcessed(context.Background(), "bm1")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	if processed {
		t.Errorf("IsProcessed after failed Apply: got true, want false")
	}
}

func TestApply_DoesNotSendRemoveLabelsEvenWhenPreExistingLabelsExist(t *testing.T) {
	// Even if the bookmark already has labels on the Readeck side
	// (e.g. "old"), we never ask Readeck to remove them — we are
	// strictly additive.
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	// First apply to seed state.
	if err := m.Apply(context.Background(), "bm1", []string{"tech"}, "model-x", 0.5); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	// Second apply — should still only add.
	if err := m.Apply(context.Background(), "bm1", []string{"ai"}, "model-y", 0.9); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if strings.Contains(stub.lastBody, "remove_labels") {
		t.Errorf("body must never contain remove_labels, got %s", stub.lastBody)
	}
	if !strings.Contains(stub.lastBody, `"add_labels":["ai"]`) {
		t.Errorf("second body should add ai, got %s", stub.lastBody)
	}
}

// --- Inventory ---------------------------------------------------------

func TestInventory_ReturnsSorted(t *testing.T) {
	stub := &readeckStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	rc := readeck.New(srv.URL, "tok", readeck.WithHTTPClient(srv.Client()))
	m := New(store, rc)

	got, err := m.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	// Stub serves tech/news/cooking. We sort alphabetically.
	want := []string{"cooking", "news", "tech"}
	if !equalStringSlices(got, want) {
		t.Errorf("Inventory: got %v, want %v", got, want)
	}
}

// --- helpers -----------------------------------------------------------

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	//nolint:gosec // G602 false positive: bounds are checked above.
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
