// SPDX-License-Identifier: GPL-3.0-or-later
//
// Package state persists per-bookmark classification outcomes and a
// label inventory to a local SQLite database.
//
// The Store is the source of truth for "what's new?" — later phases
// query IsProcessed to decide which bookmarks to classify. It is
// also where we record what labels the LLM produced, so the
// classifier prompt can prefer existing labels over inventing new
// ones.
//
// Schema lives in internal/state/migrations/*.sql and is applied by
// pressly/goose at Open time. All schema-touching code is generated
// by sqlc from internal/state/queries.sql — see sqlc.yaml.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go SQLite driver; registers "sqlite" driver name
)

// sqliteDriverName is the driver name registered by modernc.org/sqlite.
const sqliteDriverName = "sqlite"

// ErrNotFound is returned when a query yields no rows. Callers can
// use IsNotFound to test for it without coupling to the underlying
// error type.
var ErrNotFound = errors.New("state: not found")

// IsNotFound reports whether err is or wraps ErrNotFound.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// Store is the public handle on the local SQLite database. Safe for
// concurrent use — every method takes a context and delegates to the
// underlying *sql.DB which is goroutine-safe.
type Store struct {
	db   *sql.DB
	path string

	// closeOnce makes Close idempotent. Calling Close twice returns
	// nil; the second call simply does nothing.
	closeOnce sync.Once
}

// Open opens (or creates) the SQLite database at path and applies any
// pending migrations. The parent directory is auto-created if it
// doesn't exist — this matches the typical
// `~/.local/share/<app>/<db>.db` layout in the example config.
//
// On any failure the partially-opened DB is closed before returning,
// so callers can rely on a successful Open returning a fully-migrated
// Store or a clean error.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		//nolint:gosec // G301: 0755 is the right default for a
		// per-user data dir; tighter perms break multi-user
		// installations where the daemon runs as a different uid.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create state db parent dir %s: %w", dir, err)
		}
	}

	// modernc.org/sqlite understands ":memory:" and file paths. We
	// use the file path passed by the caller so the DB survives
	// process restarts.
	db, err := sql.Open(sqliteDriverName, path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	// sqlite needs a single writer at a time for schema changes;
	// modernc handles this internally but setting a small pool size
	// keeps resource use predictable.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &Store{db: db, path: path}, nil
}

// Path returns the file path the Store was opened with. Useful for
// diagnostics and for tests that want to inspect the on-disk file.
func (s *Store) Path() string { return s.path }

// Close releases the underlying database connection. Idempotent.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.db.Close()
	})
	return err
}

// runMigrations applies all pending migrations from migrationsFS
// using goose. Idempotent — calling it on an already-migrated DB
// is a no-op.
func runMigrations(db *sql.DB) error {
	// Silence goose's default stdout logger; the application uses
	// its own structured logger and we don't want migration noise
	// mixed into the user-facing log stream.
	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return err
	}
	return nil
}

// IsProcessed reports whether bookmarkID has been classified before.
//
// This is the "what's new?" filter: the daemon and `run` subcommand
// skip bookmarks that return true here (unless --force is set).
func (s *Store) IsProcessed(ctx context.Context, bookmarkID string) (bool, error) {
	v, err := s.q().IsProcessed(ctx, s.db, bookmarkID)
	if err != nil {
		return false, fmt.Errorf("is_processed: %w", err)
	}
	return v == 1, nil
}

// ProcessedDetail is a typed view of a row in processed_bookmarks.
// Labels are decoded from the stored JSON array.
type ProcessedDetail struct {
	BookmarkID    string
	ClassifiedAt  time.Time
	AppliedLabels []string
	LLMModel      string
	Confidence    float64
}
// ErrNotFound wrapped in a more specific error if no such bookmark
// has been classified.
func (s *Store) ProcessedDetail(ctx context.Context, bookmarkID string) (ProcessedDetail, error) {
	row, err := s.q().GetProcessedDetail(ctx, s.db, bookmarkID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProcessedDetail{}, fmt.Errorf("processed %s: %w", bookmarkID, ErrNotFound)
		}
		return ProcessedDetail{}, fmt.Errorf("get_processed_detail: %w", err)
	}

	labels, err := decodeLabels(row.AppliedLabels)
	if err != nil {
		return ProcessedDetail{}, fmt.Errorf("decode applied_labels for %s: %w", bookmarkID, err)
	}

	ts, err := time.Parse(time.RFC3339Nano, row.ClassifiedAt)
	if err != nil {
		return ProcessedDetail{}, fmt.Errorf("parse classified_at for %s: %w", bookmarkID, err)
	}

	return ProcessedDetail{
		BookmarkID:    row.BookmarkID,
		ClassifiedAt:  ts,
		AppliedLabels: labels,
		LLMModel:      row.LlmModel,
		Confidence:    row.Confidence,
	}, nil
}

// DeleteProcessed removes the processed_bookmarks row for
// bookmarkID. The classify subcommand uses this to force a
// re-classification of a specific bookmark. Returns nil if the
// row didn't exist (idempotent).
func (s *Store) DeleteProcessed(ctx context.Context, bookmarkID string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM processed_bookmarks WHERE bookmark_id = ?`, bookmarkID,
	); err != nil {
		return fmt.Errorf("delete processed %s: %w", bookmarkID, err)
	}
	return nil
}

// WipeProcessed drops every row in processed_bookmarks. Used by
// `backfill --force` to make the next pass re-classify everything.
// The label_inventory is preserved — we don't lose the label
// usage counts just because we're starting fresh.
func (s *Store) WipeProcessed(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM processed_bookmarks`); err != nil {
		return fmt.Errorf("wipe processed: %w", err)
	}
	return nil
}

// MarkProcessed records that bookmarkID has been classified with
// labels (using model at confidence). Replaces any prior record for
// the same ID — callers using --force re-classification rely on
// this overwriting semantics.
//
// Labels are normalised before storage: trimmed, lower-cased, and
// de-duplicated, so the same label written three different ways
// still shows up once.
func (s *Store) MarkProcessed(ctx context.Context, bookmarkID string, labels []string, model string, confidence float64) error {
	normalised := normaliseLabels(labels)

	payload, err := encodeLabels(normalised)
	if err != nil {
		return fmt.Errorf("encode applied_labels: %w", err)
	}

	err = s.q().UpsertProcessed(ctx, s.db, UpsertProcessedParams{
		BookmarkID:    bookmarkID,
		ClassifiedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		AppliedLabels: payload,
		LlmModel:      model,
		Confidence:    confidence,
	})
	if err != nil {
		return fmt.Errorf("upsert_processed: %w", err)
	}
	return nil
}

// LabelStat describes a single label in the inventory.
type LabelStat struct {
	Name      string
	FirstSeen time.Time
	UseCount  int
}

// RecordLabel bumps the use_count for name (creating it on first
// use). Name is normalised the same way as MarkProcessed does for
// labels — trimmed and lower-cased — so "Tech" and " tech " share
// a row.
func (s *Store) RecordLabel(ctx context.Context, name string) error {
	normalised, ok := normaliseLabelName(name)
	if !ok {
		return nil // empty after normalisation → silently skip
	}

	err := s.q().UpsertLabel(ctx, s.db, UpsertLabelParams{
		Name:      normalised,
		FirstSeen: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return fmt.Errorf("upsert_label: %w", err)
	}
	return nil
}

// ListLabels returns every label in the inventory, ordered by
// use_count descending (ties broken by name ascending). Returns an
// empty slice — never nil — when the inventory is empty.
func (s *Store) ListLabels(ctx context.Context) ([]LabelStat, error) {
	rows, err := s.q().ListLabels(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("list_labels: %w", err)
	}

	out := make([]LabelStat, 0, len(rows))
	for _, r := range rows {
		ts, err := time.Parse(time.RFC3339Nano, r.FirstSeen)
		if err != nil {
			return nil, fmt.Errorf("parse first_seen for %s: %w", r.Name, err)
		}
		out = append(out, LabelStat{
			Name:      r.Name,
			FirstSeen: ts,
			UseCount:  int(r.UseCount),
		})
	}
	return out, nil
}

// q returns the sqlc-generated Queries handle. Indirected through a
// method so we can swap to a mock in tests later without rewiring
// every callsite.
func (s *Store) q() *Queries { return &Queries{} }

// normaliseLabels trims, lower-cases, dedupes, and drops empties.
// Order is preserved (first occurrence wins).
func normaliseLabels(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		n := strings.ToLower(strings.TrimSpace(raw))
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// normaliseLabelName is normaliseLabels for a single string. The
// bool reports whether the result is non-empty.
func normaliseLabelName(s string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(s))
	return n, n != ""
}

// encodeLabels marshals labels to a JSON array string for storage.
func encodeLabels(labels []string) (string, error) {
	if labels == nil {
		labels = []string{}
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeLabels parses a stored JSON array back into a Go slice.
// On parse error it returns an error wrapping the offending payload
// — we never silently drop labels because losing them changes
// classifier behaviour.
//
// Order from storage is preserved; callers that want a sorted view
// can sort themselves.
func decodeLabels(s string) ([]string, error) {
	if s == "" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}
