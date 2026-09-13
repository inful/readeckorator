// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the local SQLite state store.
//
// The state store tracks which bookmarks have been classified (and
// with what labels), plus an inventory of every label the LLM has
// ever produced. It is the source of truth for "what's new?" in
// later phases.
//
// Tests use a real SQLite file under t.TempDir() so we exercise the
// same driver (modernc.org/sqlite) that ships in production. We
// never reach into the DB directly from tests — every assertion
// goes through the public Store API so the tests stay valid as the
// internal sqlc-generated query methods evolve.

package state

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestStore returns a fresh, migrated Store backed by a file under
// t.TempDir(). It registers a cleanup to close the DB. Tests that need
// to inspect the DB path can use store.Path().
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func TestOpen_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got, want := s.Path(), path; got != want {
		t.Errorf("Path: got %q, want %q", got, want)
	}
}

func TestOpen_RunsMigrations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// After Open, every table in the schema must exist.
	wantTables := []string{"processed_bookmarks", "label_inventory", "goose_db_version"}
	for _, tbl := range wantTables {
		var name string
		err := s.db.QueryRowContext(context.Background(),
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q: %v", tbl, err)
		}
		if name != tbl {
			t.Errorf("table %q: got %q, want %q", tbl, name, tbl)
		}
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := s1.MarkProcessed(context.Background(), "abc", []string{"tech"}, "model-x", 0.9); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	// Reopen — should not duplicate-process the existing row.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, err := s2.IsProcessed(context.Background(), "abc")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	if !got {
		t.Errorf("IsProcessed after reopen: got false, want true")
	}
}

func TestOpen_MissingParentDirectoryIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does", "not", "exist", "state.db")
	_, err := Open(path)
	if err == nil {
		t.Fatalf("Open: expected error for missing parent dir, got nil")
	}
	if !strings.Contains(err.Error(), "does/not/exist") {
		t.Errorf("error should mention the path, got: %v", err)
	}
}

func TestIsProcessed_UnknownReturnsFalse(t *testing.T) {
	s := newTestStore(t)
	got, err := s.IsProcessed(context.Background(), "never-seen")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	if got {
		t.Errorf("IsProcessed: got true, want false")
	}
}

func TestMarkProcessed_ThenIsProcessed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.MarkProcessed(ctx, "bm1", []string{"tech", "ai"}, "model-x", 0.92); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}

	got, err := s.IsProcessed(ctx, "bm1")
	if err != nil {
		t.Fatalf("IsProcessed: %v", err)
	}
	if !got {
		t.Errorf("IsProcessed: got false, want true")
	}
}

func TestMarkProcessed_OverwritesExistingRow(t *testing.T) {
	// MarkProcessed should be idempotent: calling it twice with
	// different labels replaces the prior classification. This is
	// the behavior we want when --force re-classifies a bookmark.
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.MarkProcessed(ctx, "bm1", []string{"tech"}, "model-x", 0.5); err != nil {
		t.Fatalf("MarkProcessed #1: %v", err)
	}
	if err := s.MarkProcessed(ctx, "bm1", []string{"news", "world"}, "model-y", 0.8); err != nil {
		t.Fatalf("MarkProcessed #2: %v", err)
	}

	// The detail accessor should reflect the second call's labels.
	detail, err := s.ProcessedDetail(ctx, "bm1")
	if err != nil {
		t.Fatalf("ProcessedDetail: %v", err)
	}
	if got, want := detail.LLMModel, "model-y"; got != want {
		t.Errorf("LLMModel: got %q, want %q", got, want)
	}
	if got, want := detail.Confidence, 0.8; got != want {
		t.Errorf("Confidence: got %v, want %v", got, want)
	}
	if got := strings.Join(detail.AppliedLabels, ","); got != "news,world" {
		t.Errorf("AppliedLabels: got %q, want %q", got, "news,world")
	}
}

func TestMarkProcessed_NormalisesLabels(t *testing.T) {
	// Labels must be stored lower-cased and trimmed; duplicates are
	// collapsed. This keeps the inventory tidy without relying on
	// the LLM to be consistent.
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.MarkProcessed(ctx, "bm1",
		[]string{"Tech", " tech ", "TECH", "ai"},
		"model-x", 0.9,
	); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}

	detail, err := s.ProcessedDetail(ctx, "bm1")
	if err != nil {
		t.Fatalf("ProcessedDetail: %v", err)
	}
	want := []string{"tech", "ai"}
	if len(detail.AppliedLabels) != len(want) {
		t.Fatalf("AppliedLabels: got %v, want %v", detail.AppliedLabels, want)
	}
	for i, w := range want {
		if detail.AppliedLabels[i] != w {
			t.Errorf("AppliedLabels[%d]: got %q, want %q", i, detail.AppliedLabels[i], w)
		}
	}
}

func TestProcessedDetail_UnknownReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.ProcessedDetail(context.Background(), "never-seen")
	if err == nil {
		t.Fatalf("ProcessedDetail: expected error for unknown id, got nil")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound: got false, want true (err=%v)", err)
	}
}

func TestRecordLabel_NewLabel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.RecordLabel(ctx, "tech"); err != nil {
		t.Fatalf("RecordLabel: %v", err)
	}

	stats, err := s.ListLabels(ctx)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("ListLabels: got %d, want 1", len(stats))
	}
	if stats[0].Name != "tech" {
		t.Errorf("Name: got %q, want %q", stats[0].Name, "tech")
	}
	if stats[0].UseCount != 1 {
		t.Errorf("UseCount: got %d, want 1", stats[0].UseCount)
	}
	if stats[0].FirstSeen.IsZero() {
		t.Errorf("FirstSeen: got zero, want non-zero")
	}
}

func TestRecordLabel_IncrementsExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.RecordLabel(ctx, "tech"); err != nil {
			t.Fatalf("RecordLabel #%d: %v", i, err)
		}
	}

	stats, err := s.ListLabels(ctx)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("ListLabels: got %d, want 1", len(stats))
	}
	if stats[0].UseCount != 3 {
		t.Errorf("UseCount: got %d, want 3", stats[0].UseCount)
	}
}

func TestRecordLabel_PreservesFirstSeen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.RecordLabel(ctx, "tech"); err != nil {
		t.Fatalf("RecordLabel #1: %v", err)
	}
	first := mustFirstSeen(t, s, "tech")

	// Sleep enough for the timestamp to change at the second
	// resolution that SQLite stores.
	time.Sleep(1100 * time.Millisecond)

	if err := s.RecordLabel(ctx, "tech"); err != nil {
		t.Fatalf("RecordLabel #2: %v", err)
	}
	second := mustFirstSeen(t, s, "tech")

	if !second.Equal(first) {
		t.Errorf("FirstSeen changed across updates: was %v, now %v", first, second)
	}
}

func TestRecordLabel_NormalisesName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"Tech", " tech ", "TECH"} {
		if err := s.RecordLabel(ctx, name); err != nil {
			t.Fatalf("RecordLabel(%q): %v", name, err)
		}
	}

	stats, err := s.ListLabels(ctx)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("ListLabels: got %d labels, want 1 (collapsed)", len(stats))
	}
	if stats[0].Name != "tech" {
		t.Errorf("Name: got %q, want %q", stats[0].Name, "tech")
	}
	if stats[0].UseCount != 3 {
		t.Errorf("UseCount: got %d, want 3", stats[0].UseCount)
	}
}

func TestListLabels_SortedByUseCountDesc(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	recordN(t, s, "rare", 1)
	recordN(t, s, "common", 5)
	recordN(t, s, "medium", 3)

	stats, err := s.ListLabels(ctx)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	want := []string{"common", "medium", "rare"}
	if len(stats) != len(want) {
		t.Fatalf("ListLabels: got %d, want %d", len(stats), len(want))
	}
	for i, w := range want {
		if stats[i].Name != w {
			t.Errorf("ListLabels[%d]: got %q, want %q", i, stats[i].Name, w)
		}
	}
}

func TestListLabels_Empty(t *testing.T) {
	s := newTestStore(t)
	stats, err := s.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if stats == nil {
		t.Errorf("ListLabels: got nil, want empty slice")
	}
	if len(stats) != 0 {
		t.Errorf("ListLabels: got %d, want 0", len(stats))
	}
}

func TestClose_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	// Second Close should be a no-op (or at least not panic).
	if err := s.Close(); err != nil {
		t.Errorf("Close #2: %v", err)
	}
}

// helpers

func recordN(t *testing.T, s *Store, name string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.RecordLabel(context.Background(), name); err != nil {
			t.Fatalf("RecordLabel(%q, %d): %v", name, i, err)
		}
	}
}

func mustFirstSeen(t *testing.T, s *Store, name string) time.Time {
	t.Helper()
	stats, err := s.ListLabels(context.Background())
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	for _, st := range stats {
		if st.Name == name {
			return st.FirstSeen
		}
	}
	t.Fatalf("label %q not found", name)
	return time.Time{}
}
