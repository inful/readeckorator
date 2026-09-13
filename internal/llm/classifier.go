// SPDX-License-Identifier: GPL-3.0-or-later

package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ClassificationResult is the structured output of the LLM for a
// single bookmark. The LLM is asked to return this as JSON; we
// parse it into this struct before applying labels.
type ClassificationResult struct {
	Labels      []string `json:"labels"`
	Collections []string `json:"collections"`
	Confidence  float64  `json:"confidence"`
	Reasoning   string   `json:"reasoning"`
}

// ParseClassification decodes raw JSON content from the LLM into
// a ClassificationResult, normalising the label names (lower-case,
// trimmed, deduped) and clamping confidence to [0, 1] so a flaky
// model can't trick downstream code into applying labels above the
// configured threshold.
//
// Collections names are trimmed and deduped but case is preserved —
// they must match the names declared in config exactly.
//
// When the LLM response isn't valid JSON — most often because the
// gateway returned an HTML error page or auth wall — we surface a
// helpful hint that names the likely cause instead of a
// cryptic json.UnmarshalSyntaxError.
func ParseClassification(raw string) (ClassificationResult, error) {
	var got ClassificationResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		return got, classifyParseError(raw, err)
	}
	got.Labels = normaliseLabels(got.Labels)
	got.Collections = normaliseCollectionNames(got.Collections)

	if got.Confidence < 0 {
		got.Confidence = 0
	}
	if got.Confidence > 1 {
		got.Confidence = 1
	}
	return got, nil
}

// classifyParseError converts a raw "is this JSON?" failure into a
// clearer diagnostic. The two cases we see in the wild are:
//   - HTML: the gateway returned an error page (auth, rate limit,
//     502, etc.) — usually caused by a bad API key or wrong model.
//   - plain text: the model itself declined or refused (safety
//     filters, output limits).
// Anything else falls back to the raw json error.
func classifyParseError(raw string, jsonErr error) error {
	trimmed := strings.TrimSpace(raw)
	preview := trimmed
	if len(preview) > 120 {
		preview = preview[:120] + "…"
	}

	switch {
	case strings.HasPrefix(strings.ToLower(trimmed), "<!doctype") ||
		strings.HasPrefix(strings.ToLower(trimmed), "<html"):
		return fmt.Errorf(
			"expected JSON from LLM but got an HTML page — "+
				"check the API key, model name, and endpoint URL. "+
				"First line: %q. Underlying error: %w",
			firstLine(trimmed), jsonErr,
		)
	case strings.HasPrefix(trimmed, "<"):
		return fmt.Errorf(
			"expected JSON from LLM but got HTML — "+
				"this usually means the gateway returned an error page "+
				"(auth wall, bad key, wrong model, 5xx). Preview: %q. "+
				"Underlying error: %w",
			preview, jsonErr,
		)
	case !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "["):
		return fmt.Errorf(
			"expected JSON from LLM but got plain text — "+
				"the model may have refused to answer or hit a safety filter. "+
				"Preview: %q. Underlying error: %w",
			preview, jsonErr,
		)
	default:
		return fmt.Errorf("parse classification JSON: %w", jsonErr)
	}
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// normaliseLabels trims, lower-cases, and dedupes a label list.
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

// normaliseCollectionNames trims and dedupes collection names
// while preserving case (they must match config exactly).
func normaliseCollectionNames(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		key := strings.ToLower(n)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, n)
	}
	return out
}

// CollectionGroup is a label → collection mapping from the config.
// Used by the system prompt to tell the LLM which labels should
// route into which collection.
type CollectionGroup struct {
	Name   string
	Labels []string
}

// BuildSystemPromptInput carries every knob that affects the
// system prompt. Kept as a struct (rather than positional args) so
// adding a new knob doesn't break every callsite.
type BuildSystemPromptInput struct {
	ExistingLabels       []string
	CollectionGroups     []CollectionGroup
	PreferExisting       bool
	AllowNewLabels       bool
	MaxLabelsPerBookmark int
}

// BuildSystemPrompt renders the system prompt sent to the LLM on
// every classification call. It includes:
//   - the JSON schema the LLM is expected to return
//   - the existing label inventory (so the LLM prefers them)
//   - the configured collection groups (so the LLM routes labels)
//   - policy guidance: additive, normalise names, etc.
//
// The prompt is deterministic for a given input — we sort label
// lists so changes to map iteration order don't reshuffle the
// prompt.
func BuildSystemPrompt(in BuildSystemPromptInput) string {
	var b strings.Builder

	b.WriteString("You are a bookmark classifier for Readeck. Given a web article's metadata and body, you assign a small set of lowercase labels from the existing inventory, and identify which configured collection(s) the bookmark belongs to.\n\n")

	b.WriteString("## Output\n\n")
	b.WriteString("Respond with strict JSON matching this schema, and nothing else:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"labels\":      [\"string\", ...],   // 1..max labels per bookmark\n")
	b.WriteString("  \"collections\": [\"string\", ...],   // names from the configured groups below\n")
	b.WriteString("  \"confidence\":  0.0..1.0,           // your overall confidence in the labelling\n")
	b.WriteString("  \"reasoning\":   \"string\"           // one-sentence justification\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")

	if len(in.CollectionGroups) > 0 {
		b.WriteString("## Configured collections\n\n")
		b.WriteString("Match the `collections` field to one of these group names. Pick from the list — do not invent new collection names.\n\n")
		// Sort by name for deterministic output.
		groups := append([]CollectionGroup(nil), in.CollectionGroups...)
		sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
		for _, g := range groups {
			fmt.Fprintf(&b, "- **%s**: labels %s\n", g.Name, formatList(g.Labels))
		}
		b.WriteString("\n")
	}

	if len(in.ExistingLabels) > 0 {
		b.WriteString("## Existing labels\n\n")
		b.WriteString("Strongly prefer reusing these existing labels over inventing new ones. Use the exact lowercase form. Only create a new label when none of the existing labels reasonably fit.\n\n")
		sorted := append([]string(nil), in.ExistingLabels...)
		sort.Strings(sorted)
		b.WriteString(formatList(sorted))
		b.WriteString("\n\n")
	} else {
		b.WriteString("## Labels\n\n")
		b.WriteString("There is no existing label inventory. Invent a short, lowercase, single-word (or kebab-case) label. Examples: `technology`, `cooking`, `world-news`.\n\n")
	}

	b.WriteString("## Policy\n\n")
	b.WriteString("- Labels are lowercase, kebab-case when multi-word. Trim whitespace before emitting.\n")
	b.WriteString("- Apply 1 to N labels where N is bounded by the configured maximum (default 5).\n")
	b.WriteString("- Be conservative: if you're not confident the article belongs to a category, don't label it as that category.\n")
	b.WriteString("- The caller adds your labels to the existing ones — never request removal of pre-existing labels.\n")
	b.WriteString("- Strongly prefer reusing existing labels over inventing new ones.\n")
	if !in.AllowNewLabels {
		b.WriteString("- Do NOT invent new labels. Pick only from the existing inventory, even if no label is a perfect fit.\n")
	}
	b.WriteString("- Confidence is your overall confidence in the entire labelling, in [0.0, 1.0]. Be honest — a low-confidence prediction is more useful than a confident wrong one.\n")
	return b.String()
}

// UserPromptInput is the metadata we send to the LLM as the user
// message. Body may be long; BuildUserPrompt will truncate it.
type UserPromptInput struct {
	Title       string
	URL         string
	Description string
	Site        string
	Body        string
	// MaxBodyChars caps the body length passed to the LLM. Zero
	// means use the default (48000).
	MaxBodyChars int
}

// BuildUserPrompt renders the user prompt. The body is truncated
// at MaxBodyChars runes (default 48000) on a word boundary.
func BuildUserPrompt(in UserPromptInput) string {
	maxChars := in.MaxBodyChars
	if maxChars <= 0 {
		maxChars = 48000
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", in.Title)
	if in.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", in.URL)
	}
	if in.Site != "" {
		fmt.Fprintf(&b, "Site: %s\n", in.Site)
	}
	if in.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", in.Description)
	}
	b.WriteString("\n---\n\n")
	if in.Body != "" {
		b.WriteString(Truncate(in.Body, maxChars))
	}
	return b.String()
}

// formatList renders a slice of strings as a comma-separated list,
// with each item quoted (for readability in prose). It does not
// short-circuit on empty input — callers should check before
// calling when an empty list would render awkwardly.
func formatList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}
