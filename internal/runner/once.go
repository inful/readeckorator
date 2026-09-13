// SPDX-License-Identifier: GPL-3.0-or-later

package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/inful/readeckorator/internal/collections"
	"github.com/inful/readeckorator/internal/readeck"
)

// Once performs a single classification pass: list candidate
// bookmarks, classify each, reconcile collections at the end.
//
// "Candidates" are bookmarks that:
//   - have loaded=true (extraction done)
//   - have loaded=false but should be retried on the next pass
//   - are NOT yet in the state DB
//
// Already-processed bookmarks are skipped (state DB lookup).
//
// Returns the summary and any fatal error. Per-bookmark errors are
// counted but don't abort the run.
func (a *AppContext) Once(ctx context.Context, logger *slog.Logger, pages int) (summary, error) {
	start := time.Now()

	if a.Cfg.Collections.ReconcileOnStart != nil && *a.Cfg.Collections.ReconcileOnStart {
		groups := make([]collections.Group, 0, len(a.Cfg.Collections.Groups))
		for _, g := range a.Cfg.Collections.Groups {
			groups = append(groups, collections.Group{Name: g.Name, Labels: g.Labels})
		}
		if err := a.Reconciler.Reconcile(ctx, groups); err != nil {
			logger.Warn("collections reconcile failed at start of run", slog.String("err", err.Error()))
		}
	}

	candidates, err := a.listCandidates(ctx, logger, pages)
	if err != nil {
		return summary{}, fmt.Errorf("list candidates: %w", err)
	}

	logger.Info("candidates gathered", slog.Int("count", len(candidates)))

	var s summary
	for _, bm := range candidates {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		result, err := a.Pipeline.Classify(ctx, bm.ID)
		if err != nil {
			s.Errors++
			logger.Error("classify failed",
				slog.String("bookmark_id", bm.ID),
				slog.String("err", err.Error()),
			)
			continue
		}
		if result.Skipped {
			s.Skipped++
			logger.Debug("bookmark skipped",
				slog.String("bookmark_id", bm.ID),
				slog.String("reason", result.SkipReason),
			)
			continue
		}
		s.Processed++
		logger.Info("bookmark classified",
			slog.String("bookmark_id", bm.ID),
			slog.Any("applied_labels", result.AppliedLabels),
			slog.Float64("confidence", result.Confidence),
		)
	}

	printSummary(logger, time.Since(start), s)
	return s, nil
}

// listCandidates fetches pages of loaded bookmarks from Readeck
// and returns every one the state DB doesn't know about yet.
//
// `pages` caps the total pages walked; the caller's --pages flag
// drives this. A value <= 0 falls back to one page (50 bookmarks).
func (a *AppContext) listCandidates(ctx context.Context, logger *slog.Logger, pages int) ([]readeck.Bookmark, error) {
	limit := a.Cfg.Readeck.PageSize
	if limit <= 0 {
		limit = 50
	}
	if pages <= 0 {
		pages = 1
	}

	var out []readeck.Bookmark
	for p := 1; p <= pages; p++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		page, err := a.Readeck.ListBookmarks(ctx, readeck.BookmarkListParams{
			Limit:   limit,
			Page:    p,
			Sort:    "created",
			IsLoaded: ptrBool(true),
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return out, err
			}
			return out, fmt.Errorf("list page %d: %w", p, err)
		}
		if len(page) == 0 {
			break
		}

		// Filter against the state DB.
		filtered := make([]readeck.Bookmark, 0, len(page))
		for _, bm := range page {
			processed, err := a.Store.IsProcessed(ctx, bm.ID)
			if err != nil {
				return out, fmt.Errorf("check processed %s: %w", bm.ID, err)
			}
			if !processed {
				filtered = append(filtered, bm)
			}
		}
		out = append(out, filtered...)
		logger.Debug("page processed",
			slog.Int("page", p),
			slog.Int("fetched", len(page)),
			slog.Int("new", len(filtered)),
		)
	}
	return out, nil
}

func ptrBool(v bool) *bool { return &v }
