// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the YAML config loader.
//
// These tests are the contract for the readeckorator.yaml schema:
//   - load a valid YAML file and apply defaults
//   - interpolate ${ENV_VAR} in string fields
//   - reject missing required fields with a clear error
//   - reject invalid durations, enums, and ranges
//   - reject missing files / malformed YAML with a clear error
//
// Tests use t.TempDir for the config file and t.Setenv for env vars,
// so they do not leak state between runs.

package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// validMinimalYAML is the smallest valid config that Load accepts.
// Every section is present with at least the required keys.
const validMinimalYAML = `
readeck:
  base_url: "https://readeck.example.com"
  api_token: "literal-token"

llm:
  base_url: "https://api.example.com/v1"
  api_key: "literal-key"
  model: "test-model"

state:
  db_path: "/tmp/readeckorator.db"
`

func TestLoad_MinimalValid(t *testing.T) {
	path := writeConfig(t, validMinimalYAML)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	if got.Readeck.BaseURL != "https://readeck.example.com" {
		t.Errorf("Readeck.BaseURL: got %q", got.Readeck.BaseURL)
	}
	if got.Readeck.APIToken != "literal-token" {
		t.Errorf("Readeck.APIToken: got %q", got.Readeck.APIToken)
	}
	if got.LLM.Model != "test-model" {
		t.Errorf("LLM.Model: got %q", got.LLM.Model)
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	path := writeConfig(t, validMinimalYAML)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Readeck defaults
	if got.Readeck.Timeout != DefaultReadeckTimeout {
		t.Errorf("Readeck.Timeout: got %v, want %v", got.Readeck.Timeout, DefaultReadeckTimeout)
	}
	if got.Readeck.PageSize != DefaultReadeckPageSize {
		t.Errorf("Readeck.PageSize: got %d, want %d", got.Readeck.PageSize, DefaultReadeckPageSize)
	}

	// LLM defaults
	if got.LLM.Timeout != DefaultLLMTimeout {
		t.Errorf("LLM.Timeout: got %v, want %v", got.LLM.Timeout, DefaultLLMTimeout)
	}
	if got.LLM.Temperature != DefaultLLMTemperature {
		t.Errorf("LLM.Temperature: got %v, want %v", got.LLM.Temperature, DefaultLLMTemperature)
	}
	if got.LLM.MaxInputChars != DefaultMaxInputChars {
		t.Errorf("LLM.MaxInputChars: got %d, want %d", got.LLM.MaxInputChars, DefaultMaxInputChars)
	}

	// Classifier defaults
	if got.Classifier.MaxLabelsPerBookmark != DefaultMaxLabelsPerBookmark {
		t.Errorf("Classifier.MaxLabelsPerBookmark: got %d, want %d",
			got.Classifier.MaxLabelsPerBookmark, DefaultMaxLabelsPerBookmark)
	}
	if got.Classifier.MinConfidence != DefaultMinConfidence {
		t.Errorf("Classifier.MinConfidence: got %v, want %v",
			got.Classifier.MinConfidence, DefaultMinConfidence)
	}
	if got.Classifier.PreferExistingLabels == nil || !*got.Classifier.PreferExistingLabels {
		t.Errorf("Classifier.PreferExistingLabels: got %v, want ptr-to-true", got.Classifier.PreferExistingLabels)
	}
	if got.Classifier.AllowNewLabels == nil || !*got.Classifier.AllowNewLabels {
		t.Errorf("Classifier.AllowNewLabels: got %v, want ptr-to-true", got.Classifier.AllowNewLabels)
	}

	// Collections default
	if got.Collections.ReconcileOnStart == nil || !*got.Collections.ReconcileOnStart {
		t.Errorf("Collections.ReconcileOnStart: got %v, want ptr-to-true", got.Collections.ReconcileOnStart)
	}

	// Runner defaults
	if got.Runner.Interval != DefaultRunnerInterval {
		t.Errorf("Runner.Interval: got %v, want %v", got.Runner.Interval, DefaultRunnerInterval)
	}
	if got.Runner.Jitter != DefaultRunnerJitter {
		t.Errorf("Runner.Jitter: got %v, want %v", got.Runner.Jitter, DefaultRunnerJitter)
	}
}

func TestLoad_EnvInterpolation(t *testing.T) {
	t.Setenv("TEST_READECK_TOKEN", "from-env-token")
	t.Setenv("TEST_LLM_KEY", "from-env-key")

	path := writeConfig(t, `
readeck:
  base_url: "https://readeck.example.com"
  api_token: "${TEST_READECK_TOKEN}"

llm:
  base_url: "https://api.example.com/v1"
  api_key: "${TEST_LLM_KEY}"
  model: "test-model"

state:
  db_path: "/tmp/state.db"
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Readeck.APIToken != "from-env-token" {
		t.Errorf("Readeck.APIToken: got %q, want %q", got.Readeck.APIToken, "from-env-token")
	}
	if got.LLM.APIKey != "from-env-key" {
		t.Errorf("LLM.APIKey: got %q, want %q", got.LLM.APIKey, "from-env-key")
	}
}

func TestLoad_EnvInterpolationMissingVar(t *testing.T) {
	// ${UNSET_VAR_XYZ} should cause a clear error.
	path := writeConfig(t, `
readeck:
  base_url: "https://readeck.example.com"
  api_token: "${UNSET_VAR_XYZ_THAT_SHOULD_NOT_EXIST}"

llm:
  base_url: "https://api.example.com/v1"
  api_key: "key"
  model: "model"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for unset env var, got nil")
	}
	if !strings.Contains(err.Error(), "UNSET_VAR_XYZ") {
		t.Errorf("error should mention the missing var name, got: %v", err)
	}
}

func TestLoad_MissingRequiredReadeckAPIToken(t *testing.T) {
	path := writeConfig(t, `
readeck:
  base_url: "https://readeck.example.com"

llm:
  base_url: "https://api.example.com/v1"
  api_key: "key"
  model: "model"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for missing api_token, got nil")
	}
	if !strings.Contains(err.Error(), "api_token") {
		t.Errorf("error should mention api_token, got: %v", err)
	}
}

func TestLoad_MissingRequiredLLMAPIKey(t *testing.T) {
	path := writeConfig(t, `
readeck:
  base_url: "https://readeck.example.com"
  api_token: "tok"

llm:
  base_url: "https://api.example.com/v1"
  model: "model"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for missing llm.api_key, got nil")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error should mention api_key, got: %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatalf("Load: expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist.yaml") {
		t.Errorf("error should mention the file path, got: %v", err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeConfig(t, `
readeck:
  base_url: "https://x"
  api_token: "t"
llm:
  base_url: "https://x"
  api_key: "k"
  model: "m"
  this is not: valid: yaml: at all
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for malformed YAML, got nil")
	}
}

func TestLoad_InvalidDuration(t *testing.T) {
	path := writeConfig(t, `
readeck:
  base_url: "https://x"
  api_token: "t"
  timeout: "not-a-duration"

llm:
  base_url: "https://x"
  api_key: "k"
  model: "m"
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for invalid duration, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "duration") {
		t.Errorf("error should mention duration, got: %v", err)
	}
}

func TestLoad_NegativeMaxLabels(t *testing.T) {
	path := writeConfig(t, validMinimalYAML+`
classifier:
  max_labels_per_bookmark: -1
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for negative max_labels_per_bookmark, got nil")
	}
}

func TestLoad_ClassifierConfidenceOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  float64
	}{
		{"too low", -0.1},
		{"too high", 1.1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, validMinimalYAML+`
classifier:
  min_confidence: `+floatToString(tc.val)+`
`)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load: expected error for min_confidence=%v, got nil", tc.val)
			}
		})
	}
}

func TestLoad_CollectionsGroupsParsed(t *testing.T) {
	path := writeConfig(t, validMinimalYAML+`
collections:
  groups:
    - name: "Technology"
      labels: [technology, programming, ai]
    - name: "Food"
      labels: [cooking, recipes]
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Collections.ReconcileOnStart == nil || !*got.Collections.ReconcileOnStart {
		t.Errorf("ReconcileOnStart: got %v, want ptr-to-true", got.Collections.ReconcileOnStart)
	}
	if len(got.Collections.Groups) != 2 {
		t.Fatalf("Groups: got %d, want 2", len(got.Collections.Groups))
	}
	if got.Collections.Groups[0].Name != "Technology" {
		t.Errorf("Groups[0].Name: got %q", got.Collections.Groups[0].Name)
	}
	if len(got.Collections.Groups[0].Labels) != 3 {
		t.Errorf("Groups[0].Labels: got %d, want 3", len(got.Collections.Groups[0].Labels))
	}
}

func TestLoad_EmptyCollectionsGroups(t *testing.T) {
	path := writeConfig(t, validMinimalYAML)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Collections.Groups == nil {
		t.Errorf("Groups: got nil, want empty slice")
	}
	if len(got.Collections.Groups) != 0 {
		t.Errorf("Groups: got %d, want 0", len(got.Collections.Groups))
	}
}

func TestLoad_CollectionGroupMissingName(t *testing.T) {
	path := writeConfig(t, validMinimalYAML+`
collections:
  groups:
    - labels: [a, b]
`)

	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load: expected error for group without name, got nil")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention name, got: %v", err)
	}
}

func TestLoad_LabelsSeedParsed(t *testing.T) {
	path := writeConfig(t, validMinimalYAML+`
labels:
  seed: [technology, cooking, news]
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"technology", "cooking", "news"}
	if len(got.Labels.Seed) != len(want) {
		t.Fatalf("Seed: got %v, want %v", got.Labels.Seed, want)
	}
	for i, w := range want {
		if got.Labels.Seed[i] != w {
			t.Errorf("Seed[%d]: got %q, want %q", i, got.Labels.Seed[i], w)
		}
	}
}

func TestLoad_EnvVarUnsetInNonSecretFieldAllowsEmpty(t *testing.T) {
	// ${VAR} in a non-secret field (e.g. base_url) when unset should
	// become empty string, not an error — the validation layer catches
	// missing base_urls separately. Only secret fields strictly require
	// the env var to be set.
	t.Setenv("TEST_BASE_URL", "https://from-env.example.com")

	path := writeConfig(t, `
readeck:
  base_url: "${TEST_BASE_URL}"
  api_token: "tok"

llm:
  base_url: "https://api.example.com/v1"
  api_key: "key"
  model: "model"

state:
  db_path: "/tmp/state.db"
`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Readeck.BaseURL != "https://from-env.example.com" {
		t.Errorf("BaseURL: got %q", got.Readeck.BaseURL)
	}
}

// writeConfig writes content to a fresh file under t.TempDir() and
// returns its absolute path. It fails the test on any I/O error.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "readeckorator.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	return path
}

// floatToString formats v for embedding in a YAML literal.
func floatToString(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
