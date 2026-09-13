// SPDX-License-Identifier: GPL-3.0-or-later

// Package runner turns the configured components into working
// "run once" and "serve forever" flows. The package owns the
// subcommand-level orchestration: opening the state DB, building
// all the clients, and calling into classifier / collections.
//
// The CLI subcommands in cmd/readeckorator/main.go and
// internal/cli/cli.go call into this package — they should never
// touch readeck/llm/state directly.

package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/inful/readeckorator/internal/classifier"
	"github.com/inful/readeckorator/internal/collections"
	"github.com/inful/readeckorator/internal/config"
	"github.com/inful/readeckorator/internal/labels"
	"github.com/inful/readeckorator/internal/llm"
	"github.com/inful/readeckorator/internal/readeck"
	"github.com/inful/readeckorator/internal/state"
)

// AppContext bundles every collaborator needed by a subcommand.
// It's the one-stop-shop wired up by NewApp.
type AppContext struct {
	Cfg        *config.Config
	Readeck    *readeck.Client
	LLM        *llm.Client
	Store      *state.Store
	Labels     *labels.Manager
	Pipeline   *classifier.Pipeline
	Reconciler *collections.Reconciler
}

// Close releases resources owned by AppContext. Safe to call on
// a partially-constructed context (nil fields are skipped).
func (a *AppContext) Close() error {
	if a == nil || a.Store == nil {
		return nil
	}
	return a.Store.Close()
}

// NewApp builds an AppContext from the given config path.
//
// `dryRun` is plumbed into a logger field but does NOT short-
// circuit any of the actual API calls — the per-component code
// paths observe dry-run through the pipeline result and via
// logger messages. (Phase 10 will wire full dry-run semantics.)
func NewApp(ctx context.Context, cfgPath string, logger *slog.Logger) (*AppContext, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}

	dbPath, err := expandTilde(cfg.State.DBPath)
	if err != nil {
		return nil, fmt.Errorf("expand state db path: %w", err)
	}

	store, err := state.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}

	rc := readeck.New(cfg.Readeck.BaseURL, cfg.Readeck.APIToken,
		readeck.WithTimeout(cfg.Readeck.Timeout),
	)

	l := llm.New(cfg.LLM.BaseURL, cfg.LLM.APIKey,
		llm.WithTimeout(cfg.LLM.Timeout),
	)

	mgr := labels.New(store, rc).WithInventory(cfg.Labels.Seed)

	pipe, err := classifier.New(classifier.PipelineConfig{
		Readeck: rc,
		LLM:     l,
		Store:   store,
		Labels:  mgr,
		Classifier: classifier.ClassifierConfig{
			MinConfidence:        cfg.Classifier.MinConfidence,
			MaxLabelsPerBookmark: cfg.Classifier.MaxLabelsPerBookmark,
			AllowNewLabels:       derefBool(cfg.Classifier.AllowNewLabels),
			PreferExistingLabels: derefBool(cfg.Classifier.PreferExistingLabels),
			MaxInputChars:        cfg.LLM.MaxInputChars,
		},
	})
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("build classifier pipeline: %w", err)
	}

	rec := collections.New(rc)

	logger.Info("app initialised",
		slog.String("readeck", cfg.Readeck.BaseURL),
		slog.String("llm_model", cfg.LLM.Model),
		slog.String("state_db", dbPath),
	)

	return &AppContext{
		Cfg:        cfg,
		Readeck:    rc,
		LLM:        l,
		Store:      store,
		Labels:     mgr,
		Pipeline:   pipe,
		Reconciler: rec,
	}, nil
}

// derefBool returns the bool value pointed to by p, or false if
// p is nil. Used for *bool config fields that distinguish
// "omitted" from "explicitly false".
func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// expandTilde expands a leading ~ to the user's home directory.
// Unlike os.UserHomeDir, this works for paths that don't exist yet
// (the directory will be created by state.Open if needed).
func expandTilde(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	if path[1] == '/' || path[1] == filepath.Separator {
		return filepath.Join(home, path[2:]), nil
	}
	// "~something" — not a path expansion we support; leave as-is.
	return path, nil
}

// summary is the per-run summary line printed at the end of each
// pipeline batch.
type summary struct {
	Processed int
	Skipped   int
	Errors    int
}

// printSummary logs a single line with the run's totals.
func printSummary(logger *slog.Logger, dur time.Duration, s summary) {
	logger.Info("run complete",
		slog.Int("processed", s.Processed),
		slog.Int("skipped", s.Skipped),
		slog.Int("errors", s.Errors),
		slog.Duration("duration", dur),
	)
}
