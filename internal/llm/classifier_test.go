// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the classifier prompt builder and JSON response parser.
//
// The classifier is what actually decides labels for a bookmark.
// It builds a system prompt (label inventory + collection groups)
// and a user prompt (bookmark metadata + truncated article body),
// then parses the LLM's JSON response into a ClassificationResult.
// These tests pin the prompt format so future changes are visible.

package llm

import (
	"strings"
	"testing"
)

// sampleClassificationJSON is the JSON the LLM is expected to
// return. Used as a fixture by both the prompt-builder tests and
// the response-parser tests.
const sampleClassificationJSON = `{
  "labels": ["technology", "ai"],
  "collections": ["Technology"],
  "confidence": 0.92,
  "reasoning": "Article about fine-tuning LLMs."
}`

// --- ClassificationResult ------------------------------------------------

func TestClassificationResult_PopulatesAllFields(t *testing.T) {
	got, err := ParseClassification(sampleClassificationJSON)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "technology" || got.Labels[1] != "ai" {
		t.Errorf("Labels: got %v", got.Labels)
	}
	if len(got.Collections) != 1 || got.Collections[0] != "Technology" {
		t.Errorf("Collections: got %v", got.Collections)
	}
	if got.Confidence != 0.92 {
		t.Errorf("Confidence: got %v, want 0.92", got.Confidence)
	}
	if got.Reasoning != "Article about fine-tuning LLMs." {
		t.Errorf("Reasoning: got %q", got.Reasoning)
	}
}

func TestParseClassification_InvalidJSONReturnsError(t *testing.T) {
	_, err := ParseClassification("{not json")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parsing, got: %v", err)
	}
}

func TestParseClassification_HTMLResponseGivesHelpfulHint(t *testing.T) {
	// Real-world failure mode: the LLM gateway returns an HTML
	// error page (auth wall, 5xx, bad model) instead of JSON.
	// We surface a hint that names the likely cause rather than
	// the cryptic "invalid character '<'" from json.Unmarshal.
	html := `<!DOCTYPE html>
<html><head><title>401 Unauthorized</title></head>
<body><h1>401 Unauthorized</h1></body></html>`

	_, err := ParseClassification(html)
	if err == nil {
		t.Fatalf("expected error for HTML response")
	}
	msg := err.Error()
	for _, want := range []string{"HTML", "API key", "model"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q (likely cause hint), got: %v", want, err)
		}
	}
}

func TestParseClassification_PlainTextResponseGivesHelpfulHint(t *testing.T) {
	// The model may refuse to answer (safety filter, output limit)
	// and emit prose instead of JSON.
	_, err := ParseClassification("I'm sorry, but I can't help with that.")
	if err == nil {
		t.Fatalf("expected error for plain-text response")
	}
	if !strings.Contains(err.Error(), "plain text") &&
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("error should mention refusal/safety, got: %v", err)
	}
}

func TestParseClassification_TruncatedJSONShowsUnderlyingError(t *testing.T) {
	// JSON that starts with `{` but is broken mid-way is a real
	// parser failure (truncation, mid-stream error). We surface
	// the underlying json error without the HTML/plain-text hint.
	_, err := ParseClassification(`{"labels":["tech"],"confid`)
	if err == nil {
		t.Fatalf("expected error for truncated JSON")
	}
	if strings.Contains(err.Error(), "HTML") || strings.Contains(err.Error(), "plain text") {
		t.Errorf("truncated JSON should not produce HTML/plain-text hint, got: %v", err)
	}
}

func TestParseClassification_StripsThinkingTagsAndParsesJSON(t *testing.T) {
	// Real-world failure mode: reasoning models (DeepSeek R1, o1,
	// and apparently MiniMax-M3) prepend <think>...</think> to
	// their final answer. The JSON we want lives AFTER the closing
	// tag. We strip the reasoning and parse what remains.
	withThink := `<think>The article is about Go projects. Let me list some labels.</think>
{"labels":["go","programming"],"collections":[],"confidence":0.92,"reasoning":"matches"}`

	got, err := ParseClassification(withThink)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 2 || got.Labels[0] != "go" {
		t.Errorf("Labels: got %v", got.Labels)
	}
	if got.Confidence != 0.92 {
		t.Errorf("Confidence: got %v", got.Confidence)
	}
}

func TestParseClassification_StripsMultilineThinking(t *testing.T) {
	// Some models emit multi-paragraph reasoning.
	withThink := "<think>\nLine 1 of reasoning.\n\nLine 2 with\nnewlines.\n</think>\n" +
		`{"labels":["x"],"confidence":0.5}`
	got, err := ParseClassification(withThink)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "x" {
		t.Errorf("Labels: got %v", got.Labels)
	}
}

func TestParseClassification_StripsReasoningTags(t *testing.T) {
	// Some models use <reasoning>...</reasoning> instead of <think>.
	withReasoning := `<reasoning>Let me think about labels.</reasoning>{"labels":["a"]}`
	got, err := ParseClassification(withReasoning)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 1 {
		t.Errorf("Labels: got %v", got.Labels)
	}
}

func TestParseClassification_StripsMultipleThinkingBlocks(t *testing.T) {
	multi := `<think>first</think> some prose <think>second</think> {"labels":["a"]}`
	got, err := ParseClassification(multi)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "a" {
		t.Errorf("Labels: got %v", got.Labels)
	}
}

func TestParseClassification_AcceptsEmptyArrays(t *testing.T) {
	// LLM may legitimately return no labels if it can't classify.
	got, err := ParseClassification(`{"labels":[],"collections":[],"confidence":0.1,"reasoning":"unclear"}`)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 0 {
		t.Errorf("Labels: got %v, want empty", got.Labels)
	}
	if got.Confidence != 0.1 {
		t.Errorf("Confidence: got %v", got.Confidence)
	}
}

func TestParseClassification_NormalisesLabels(t *testing.T) {
	got, err := ParseClassification(`{"labels":["Tech"," TECH ","tech"],"confidence":0.5}`)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if len(got.Labels) != 1 || got.Labels[0] != "tech" {
		t.Errorf("Labels: got %v, want [tech]", got.Labels)
	}
}

func TestParseClassification_ClampsConfidence(t *testing.T) {
	got, err := ParseClassification(`{"labels":[],"confidence":1.5}`)
	if err != nil {
		t.Fatalf("ParseClassification: %v", err)
	}
	if got.Confidence > 1.0 {
		t.Errorf("Confidence: got %v, want <= 1.0", got.Confidence)
	}
}

// --- Prompt builder -----------------------------------------------------

func TestBuildSystemPrompt_IncludesLabelInventory(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptInput{
		ExistingLabels: []string{"tech", "cooking", "news"},
		CollectionGroups: []CollectionGroup{
			{Name: "Tech", Labels: []string{"tech"}},
		},
	})
	for _, want := range []string{"tech", "cooking", "news", "Tech"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestBuildSystemPrompt_HandlesEmptyInventory(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptInput{})
	if prompt == "" {
		t.Errorf("system prompt should not be empty even with no inventory")
	}
	// Should still describe what the LLM should do.
	if !strings.Contains(strings.ToLower(prompt), "label") {
		t.Errorf("system prompt should mention labels even with empty inventory")
	}
}

func TestBuildSystemPrompt_DeclaresJSONSchema(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptInput{})
	// The schema section tells the LLM what shape to return.
	for _, want := range []string{"labels", "collections", "confidence"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt should declare schema field %q", want)
		}
	}
}

func TestBuildSystemPrompt_ExplainsAdditivePolicy(t *testing.T) {
	prompt := BuildSystemPrompt(BuildSystemPromptInput{})
	// We are explicit: prefer existing labels, create new ones
	// only when needed. The test guards against accidentally
	// removing this guidance.
	low := strings.ToLower(prompt)
	if !strings.Contains(low, "prefer") || !strings.Contains(low, "existing") {
		t.Errorf("system prompt should explain prefer-existing policy")
	}
}

func TestBuildUserPrompt_IncludesAllFields(t *testing.T) {
	prompt := BuildUserPrompt(UserPromptInput{
		Title:       "Fine-tuning LLMs",
		URL:         "https://example.com/post",
		Description: "A short summary.",
		Site:        "example.com",
		Body:        "Long article body here.",
	})
	for _, want := range []string{
		"Fine-tuning LLMs",
		"https://example.com/post",
		"A short summary.",
		"example.com",
		"Long article body here.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("user prompt missing %q", want)
		}
	}
}

func TestBuildUserPrompt_TruncatesLongBody(t *testing.T) {
	longBody := strings.Repeat("a", 50000)
	prompt := BuildUserPrompt(UserPromptInput{
		Title: "T",
		URL:   "https://x",
		Body:  longBody,
	})
	if len(prompt) >= 50000 {
		t.Errorf("user prompt length: got %d, want < 50000 (truncation should have kicked in)", len(prompt))
	}
}

func TestBuildUserPrompt_EmptyBodyOK(t *testing.T) {
	prompt := BuildUserPrompt(UserPromptInput{
		Title: "T",
		URL:   "https://x",
	})
	if prompt == "" {
		t.Errorf("user prompt should not be empty even with just title+URL")
	}
}
