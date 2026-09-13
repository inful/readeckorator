// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the runner package — the orchestration layer that
// every CLI subcommand funnels through. We use httptest fakes for
// both the Readeck and LLM endpoints so the full path
// (config → app → list → classify → apply) can be exercised
// without network access.

package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inful/readeckorator/internal/readeck"
)

// fakeReadeck handles bookmark list / get / patch / article.md
// endpoints just well enough to drive the pipeline.
type fakeReadeck struct {
	bookmarks []readeck.Bookmark
	articles  map[string]string
	patches   atomic.Int32
}

func (f *fakeReadeck) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && len(r.URL.Path) > len("/api/bookmarks/"):
			f.patches.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))

		case r.Method == http.MethodGet && len(r.URL.Path) > len("/api/bookmarks/"):
			// Strip /article.md suffix if present, then look up by ID.
			id := r.URL.Path[len("/api/bookmarks/"):]
			hasArticle := strings.HasSuffix(id, "/article.md")
			if hasArticle {
				id = strings.TrimSuffix(id, "/article.md")
			}
			for _, b := range f.bookmarks {
				if b.ID == id {
					if hasArticle {
						body, ok := f.articles[id]
						if !ok {
							http.Error(w, "no article", http.StatusNotFound)
							return
						}
						w.Header().Set("Content-Type", "text/markdown")
						_, _ = w.Write([]byte(body))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(b)
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)

		case r.Method == http.MethodGet && r.URL.Path == "/api/bookmarks":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.bookmarks)

		default:
			http.Error(w, "no route", http.StatusNotFound)
		}
	})
}

// fakeLLM returns a canned classification response.
type fakeLLM struct {
	calls      atomic.Int32
	labels     []string
	confidence float64
}

func (f *fakeLLM) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		labelsJSON, _ := json.Marshal(f.labels)
		content := `{"labels":` + string(labelsJSON) + `,"collections":[],"confidence":` +
			strconv.FormatFloat(f.confidence, 'f', -1, 64) + `,"reasoning":"test"}`
		body := `{"id":"x","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":` +
			strconvQuote(content) + `}}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// writeConfig writes a minimal valid config to a fresh file and
// returns its path. The Readeck + LLM endpoints are passed in so
// tests can wire them up however they like.
func writeConfig(t *testing.T, readeckURL, llmURL, dbPath string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "readeckorator.yaml")
	yaml := `
readeck:
  base_url: "` + readeckURL + `"
  api_token: "test-token"

llm:
  base_url: "` + llmURL + `"
  api_key: "test-key"
  model: "test-model"

state:
  db_path: "` + dbPath + `"

classifier:
  min_confidence: 0.5
  prefer_existing_labels: true
  allow_new_labels: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// buildAppFromConfig loads the config, opens the state DB at
// dbPath, and wires everything up. Returns the AppContext. The
// caller is responsible for closing it.
func buildAppFromConfig(t *testing.T, rf *fakeReadeck, lf *fakeLLM) *AppContext {
	t.Helper()

	readeckSrv := httptest.NewServer(rf.handler())
	t.Cleanup(readeckSrv.Close)
	llmSrv := httptest.NewServer(lf.handler())
	t.Cleanup(llmSrv.Close)

	dbPath := filepath.Join(t.TempDir(), "state.db")
	cfgPath := writeConfig(t, readeckSrv.URL, llmSrv.URL, dbPath)

	logger := discardLogger()
	app, err := NewApp(context.Background(), cfgPath, logger)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return app
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- Once ----------------------------------------------------------------

func TestOnce_ProcessesUnprocessedBookmarks(t *testing.T) {
	rf := &fakeReadeck{
		bookmarks: []readeck.Bookmark{
			{ID: "bm1", Title: "T1", Loaded: true, Type: "article"},
			{ID: "bm2", Title: "T2", Loaded: true, Type: "article"},
		},
		articles: map[string]string{"bm1": "body1", "bm2": "body2"},
	}
	lf := &fakeLLM{labels: []string{"tech"}, confidence: 0.9}
	app := buildAppFromConfig(t, rf, lf)
	defer func() { _ = app.Close() }()

	ctx := context.Background()
	s, err := app.Once(ctx, discardLogger(), 1)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if s.Processed != 2 {
		t.Errorf("Processed: got %d, want 2", s.Processed)
	}
	if s.Skipped != 0 {
		t.Errorf("Skipped: got %d, want 0", s.Skipped)
	}
	if got := lf.calls.Load(); got != 2 {
		t.Errorf("LLM calls: got %d, want 2", got)
	}
	if got := rf.patches.Load(); got != 2 {
		t.Errorf("Patches: got %d, want 2", got)
	}
}

func TestOnce_SkipsAlreadyProcessed(t *testing.T) {
	rf := &fakeReadeck{
		bookmarks: []readeck.Bookmark{
			{ID: "bm1", Title: "T1", Loaded: true},
		},
		articles: map[string]string{"bm1": "body1"},
	}
	lf := &fakeLLM{labels: []string{"tech"}, confidence: 0.9}
	app := buildAppFromConfig(t, rf, lf)
	defer func() { _ = app.Close() }()

	ctx := context.Background()
	_, err := app.Once(ctx, discardLogger(), 1)
	if err != nil {
		t.Fatalf("first Once: %v", err)
	}
	s, err := app.Once(ctx, discardLogger(), 1)
	if err != nil {
		t.Fatalf("second Once: %v", err)
	}
	if s.Processed != 0 {
		t.Errorf("Processed on second pass: got %d, want 0", s.Processed)
	}
	if got := lf.calls.Load(); got != 1 {
		t.Errorf("LLM calls across two passes: got %d, want 1", got)
	}
}

func TestOnce_NotLoadedIsSkipped(t *testing.T) {
	rf := &fakeReadeck{
		bookmarks: []readeck.Bookmark{
			{ID: "bm1", Title: "T1", Loaded: false},
		},
		articles: map[string]string{},
	}
	lf := &fakeLLM{labels: []string{"x"}, confidence: 0.9}
	app := buildAppFromConfig(t, rf, lf)
	defer func() { _ = app.Close() }()

	s, err := app.Once(context.Background(), discardLogger(), 1)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if s.Processed != 0 {
		t.Errorf("Processed: got %d, want 0", s.Processed)
	}
	if got := lf.calls.Load(); got != 0 {
		t.Errorf("LLM calls when not loaded: got %d, want 0", got)
	}
}

// --- Daemon --------------------------------------------------------------

func TestDaemon_RunsOnceImmediatelyThenStopsOnCancel(t *testing.T) {
	rf := &fakeReadeck{
		bookmarks: []readeck.Bookmark{
			{ID: "bm1", Title: "T1", Loaded: true},
		},
		articles: map[string]string{"bm1": "body"},
	}
	lf := &fakeLLM{labels: []string{"tech"}, confidence: 0.9}
	app := buildAppFromConfig(t, rf, lf)
	defer func() { _ = app.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		d := NewDaemon(app, discardLogger(), 50*time.Millisecond, 0, 1)
		done <- d.Run(ctx)
	}()
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("daemon: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("daemon did not exit after cancel")
	}
	if got := lf.calls.Load(); got < 1 {
		t.Errorf("LLM calls: got %d, want >=1", got)
	}
}
