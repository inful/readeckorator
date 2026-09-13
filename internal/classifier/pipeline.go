// SPDX-License-Identifier: GPL-3.0-or-later

// Package classifier orchestrates the per-bookmark classification
// flow: fetch detail → check skip conditions → fetch article body
// → build prompt → call LLM → parse → filter → apply.
//
// The pipeline is the only place that owns the cross-component
// sequencing. Lower-level packages (readeck, llm, state, labels)
// are intentionally side-effect free in isolation — this package
// is where the "what happens when?" lives.
//
// Idempotency contract:
//
//   - If the bookmark is already in processed_bookmarks, skip.
//   - If extraction isn't done (Loaded=false), skip without
//     touching anything — the next tick will retry.
//   - If the LLM succeeds but confidence is below threshold,
//     skip the apply step but still mark the bookmark
//     processed so we don't loop forever.
//   - If the LLM call fails or the apply fails, do NOT mark
//     processed — the next pass retries.
package classifier

import (
	"context"
	"errors"
	"fmt"

	"github.com/inful/readeckorator/internal/labels"
	"github.com/inful/readeckorator/internal/llm"
	"github.com/inful/readeckorator/internal/readeck"
	"github.com/inful/readeckorator/internal/state"
)

// ClassifierConfig is the subset of config.ClassifierConfig that
// the pipeline actually reads. Kept as its own type so the
// classifier package doesn't depend on the config package.
type ClassifierConfig struct {
	MinConfidence        float64
	MaxLabelsPerBookmark int
	AllowNewLabels       bool
	PreferExistingLabels bool
	MaxInputChars        int
}

// ErrNoPipeline is returned by New when any required dependency
// is missing. Callers can use errors.Is to surface a clearer
// "check your config" message.
var ErrNoPipeline = errors.New("classifier: pipeline dependencies incomplete")

// PipelineConfig wires up all four collaborators.
type PipelineConfig struct {
	Readeck    *readeck.Client
	LLM        *llm.Client
	Store      *state.Store
	Labels     *labels.Manager
	Classifier ClassifierConfig
}

// Pipeline owns the orchestration. Safe for concurrent use as
// long as the underlying clients are.
type Pipeline struct {
	cfg PipelineConfig
}

// New constructs a Pipeline. Returns (nil, ErrNoPipeline) when any
// required field is missing so callers can distinguish "config
// problem" from a successful zero-value.
func New(cfg PipelineConfig) (*Pipeline, error) {
	if cfg.Readeck == nil || cfg.LLM == nil || cfg.Store == nil || cfg.Labels == nil {
		return nil, ErrNoPipeline
	}
	return &Pipeline{cfg: cfg}, nil
}

// Result is what Classify returns. Skipped/SkipReason describe
// the no-op cases so callers can log them.
type Result struct {
	BookmarkID    string
	Skipped       bool
	SkipReason    string
	AppliedLabels []string
	Collections   []string
	Confidence    float64
	Reasoning     string
}

// Classify runs the full classification flow for one bookmark ID.
//
// Returns (Result, error):
//   - Skipped=true, err=nil for known no-op cases (already
//     processed, not loaded, low confidence). The caller can
//     distinguish them via SkipReason.
//   - Skipped=false, err=nil on success (which may include
//     "LLM said no labels" — the bookmark is still recorded).
//   - err!=nil on infrastructure failure (Readeck 5xx, LLM 5xx
//     after retries, etc.). The bookmark is NOT marked processed
//     in this case, so the next pass retries.
func (p *Pipeline) Classify(ctx context.Context, bookmarkID string) (Result, error) {
	res := Result{BookmarkID: bookmarkID}

	// 1. Already processed? Skip.
	processed, err := p.cfg.Store.IsProcessed(ctx, bookmarkID)
	if err != nil {
		return res, fmt.Errorf("check processed: %w", err)
	}
	if processed {
		res.Skipped = true
		res.SkipReason = "already processed"
		return res, nil
	}

	// 2. Fetch bookmark detail.
	bm, err := p.cfg.Readeck.GetBookmark(ctx, bookmarkID)
	if err != nil {
		return res, fmt.Errorf("get bookmark: %w", err)
	}

	// 3. Not loaded? Skip without touching state.
	if !bm.Loaded {
		res.Skipped = true
		res.SkipReason = "bookmark not yet loaded"
		return res, nil
	}

	// 4. Fetch the article body.
	body, err := p.cfg.Readeck.GetArticleMarkdown(ctx, bookmarkID)
	if err != nil {
		return res, fmt.Errorf("get article markdown: %w", err)
	}

	// 5. Build prompt and call the LLM.
	inventory, err := p.cfg.Labels.Inventory(ctx)
	if err != nil {
		return res, fmt.Errorf("load label inventory: %w", err)
	}
	systemPrompt := llm.BuildSystemPrompt(llm.BuildSystemPromptInput{
		ExistingLabels:       inventory,
		AllowNewLabels:       p.cfg.Classifier.AllowNewLabels,
		PreferExisting:       p.cfg.Classifier.PreferExistingLabels,
		MaxLabelsPerBookmark: p.cfg.Classifier.MaxLabelsPerBookmark,
	})
	userPrompt := llm.BuildUserPrompt(llm.UserPromptInput{
		Title:        bm.Title,
		URL:          bm.URL,
		Description:  bm.Description,
		Site:         bm.SiteName,
		Body:         body,
		MaxBodyChars: p.cfg.Classifier.MaxInputChars,
	})

	chatResp, err := p.cfg.LLM.Chat(ctx, llm.ChatRequest{
		Model:       "", // caller fills in via config in a later phase
		Temperature: 0.0,
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	})
	if err != nil {
		// Don't mark processed; the next pass will retry.
		return res, fmt.Errorf("llm chat: %w", err)
	}

	// 6. Parse the JSON response.
	classification, err := llm.ParseClassification(chatResp.Content())
	if err != nil {
		return res, fmt.Errorf("parse classification: %w", err)
	}

	res.Confidence = classification.Confidence
	res.Collections = classification.Collections
	res.Reasoning = classification.Reasoning

	// 7. Below threshold? Record but don't apply.
	if p.cfg.Classifier.MinConfidence > 0 && classification.Confidence < p.cfg.Classifier.MinConfidence {
		if err := p.cfg.Store.MarkProcessed(ctx, bookmarkID, nil, "", classification.Confidence); err != nil {
			return res, fmt.Errorf("mark low-confidence processed: %w", err)
		}
		res.Skipped = true
		res.SkipReason = fmt.Sprintf("confidence %.2f below threshold %.2f",
			classification.Confidence, p.cfg.Classifier.MinConfidence)
		return res, nil
	}

	// 8. Filter labels (normalise + inventory policy).
	filtered := p.cfg.Labels.Filter(classification.Labels, p.cfg.Classifier.AllowNewLabels)
	res.AppliedLabels = filtered

	// 9. Apply via the labels manager. Apply is additive on Readeck
	// and records the outcome in the state DB. Empty filtered
	// slice is a successful "LLM said no labels" — we still want
	// to record the bookmark as processed so we don't re-prompt
	// the LLM for it.
	if err := p.cfg.Labels.Apply(ctx, bookmarkID, filtered, chatResp.Model, classification.Confidence); err != nil {
		return res, fmt.Errorf("apply labels: %w", err)
	}

	return res, nil
}
