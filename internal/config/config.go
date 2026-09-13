// SPDX-License-Identifier: GPL-3.0-or-later
//
// Package config loads, interpolates, and validates the readeckorator
// YAML configuration file.
//
// Schema is documented in configs/readeckorator.example.yaml. Loading
// proceeds in four stages:
//
//  1. Read the file from disk.
//  2. Expand ${ENV_VAR} references in the raw YAML text. Unset env
//     vars are a hard error at this stage so the user gets a clear
//     "did you forget to set FOO?" message instead of a downstream
//     "field is empty" complaint.
//  3. Unmarshal into the Config struct and apply defaults for any
//     zero-valued fields.
//  4. Validate that all required fields are present and well-formed.

package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults applied when a field is omitted from the YAML file.
const (
	DefaultReadeckTimeout       = 30 * time.Second
	DefaultReadeckPageSize      = 50
	DefaultLLMTimeout           = 60 * time.Second
	DefaultLLMTemperature       = 0.0
	DefaultMaxInputChars        = 48000
	DefaultMaxLabelsPerBookmark = 5
	DefaultMinConfidence        = 0.5
	DefaultRunnerInterval       = 5 * time.Minute
	DefaultRunnerJitter         = 30 * time.Second
)

// envVarPattern matches ${NAME} where NAME is a POSIX env-var name.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Config is the fully-resolved readeckorator configuration. Every
// field is guaranteed non-zero unless its zero value is meaningful
// (e.g. empty slice for no collection groups).
type Config struct {
	Readeck     ReadeckConfig     `yaml:"readeck"`
	LLM         LLMConfig         `yaml:"llm"`
	State       StateConfig       `yaml:"state"`
	Classifier  ClassifierConfig  `yaml:"classifier"`
	Labels      LabelsConfig      `yaml:"labels"`
	Collections CollectionsConfig `yaml:"collections"`
	Runner      RunnerConfig      `yaml:"runner"`
}

// ReadeckConfig holds the connection settings for the user's
// self-hosted Readeck instance.
type ReadeckConfig struct {
	BaseURL  string        `yaml:"base_url"`
	APIToken string        `yaml:"api_token"`
	Timeout  time.Duration `yaml:"timeout"`
	PageSize int           `yaml:"page_size"`
}

// LLMConfig holds the OpenAI-compatible endpoint and per-request tuning.
type LLMConfig struct {
	BaseURL      string        `yaml:"base_url"`
	APIKey       string        `yaml:"api_key"`
	Model        string        `yaml:"model"`
	Timeout      time.Duration `yaml:"timeout"`
	Temperature  float64       `yaml:"temperature"`
	MaxInputChars int          `yaml:"max_input_chars"`
}

// StateConfig describes the local SQLite state database.
type StateConfig struct {
	DBPath string `yaml:"db_path"`
}

// ClassifierConfig tunes the LLM-driven classification pass.
//
// The PreferExistingLabels and AllowNewLabels fields are pointers so
// we can distinguish "omitted" (nil → default true) from "explicitly
// false" (pointer to false).
type ClassifierConfig struct {
	MaxLabelsPerBookmark int      `yaml:"max_labels_per_bookmark"`
	MinConfidence        float64  `yaml:"min_confidence"`
	PreferExistingLabels *bool    `yaml:"prefer_existing_labels"`
	AllowNewLabels       *bool    `yaml:"allow_new_labels"`
}

// LabelsConfig holds the optional seed label inventory that the LLM
// sees in its system prompt.
type LabelsConfig struct {
	Seed []string `yaml:"seed"`
}

// CollectionsConfig describes how Readeck collections are reconciled
// from the configured label groups.
//
// ReconcileOnStart is a pointer so we can distinguish "omitted" (nil →
// default true) from "explicitly false".
type CollectionsConfig struct {
	ReconcileOnStart *bool             `yaml:"reconcile_on_start"`
	Groups           []CollectionGroup `yaml:"groups"`
}

// CollectionGroup maps a group of labels to a single Readeck
// collection. The collection's `labels` filter is the comma-joined
// label list.
type CollectionGroup struct {
	Name   string   `yaml:"name"`
	Labels []string `yaml:"labels"`
}

// RunnerConfig is used only by `readeckorator serve`.
type RunnerConfig struct {
	Interval time.Duration `yaml:"interval"`
	Jitter   time.Duration `yaml:"jitter"`
}

// Load reads, interpolates, parses, defaults, and validates the YAML
// config at path. On success, the returned Config is ready for use.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path comes from CLI flag, not user input
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	expanded, err := expandEnv(string(raw))
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("config %s: parse YAML: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	return cfg, nil
}

// expandEnv replaces every ${NAME} with the value of the env var NAME.
// Unset vars produce a single collected error returned at the end.
// This is intentionally stricter than os.ExpandEnv: a silent empty
// string is much harder to debug than "you forgot to set FOO".
func expandEnv(s string) (string, error) {
	var firstErr error
	result := envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// match is "${NAME}"; strip the braces.
		name := match[2 : len(match)-1]
		val, ok := os.LookupEnv(name)
		if !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("environment variable ${%s} is not set", name)
			}
			return match // leave in place; we'll return firstErr below
		}
		return val
	})
	if firstErr != nil {
		return "", firstErr
	}
	return result, nil
}

// applyDefaults fills in zero-valued fields with the documented
// defaults. Duration zero is treated as "use default" — this matches
// the convention used everywhere in the schema.
//
// Pointer-typed bool fields (PreferExistingLabels, AllowNewLabels,
// ReconcileOnStart) default to true when nil — this matches the
// documented behavior in configs/readeckorator.example.yaml.
func (c *Config) applyDefaults() {
	if c.Readeck.Timeout == 0 {
		c.Readeck.Timeout = DefaultReadeckTimeout
	}
	if c.Readeck.PageSize == 0 {
		c.Readeck.PageSize = DefaultReadeckPageSize
	}
	if c.LLM.Timeout == 0 {
		c.LLM.Timeout = DefaultLLMTimeout
	}
	if c.LLM.Temperature == 0 {
		c.LLM.Temperature = DefaultLLMTemperature
	}
	if c.LLM.MaxInputChars == 0 {
		c.LLM.MaxInputChars = DefaultMaxInputChars
	}
	if c.Classifier.MaxLabelsPerBookmark == 0 {
		c.Classifier.MaxLabelsPerBookmark = DefaultMaxLabelsPerBookmark
	}
	if c.Classifier.MinConfidence == 0 {
		c.Classifier.MinConfidence = DefaultMinConfidence
	}
	if c.Classifier.PreferExistingLabels == nil {
		tru := true
		c.Classifier.PreferExistingLabels = &tru
	}
	if c.Classifier.AllowNewLabels == nil {
		tru := true
		c.Classifier.AllowNewLabels = &tru
	}
	if c.Collections.ReconcileOnStart == nil {
		tru := true
		c.Collections.ReconcileOnStart = &tru
	}
	if c.Collections.Groups == nil {
		c.Collections.Groups = []CollectionGroup{}
	}
	if c.Runner.Interval == 0 {
		c.Runner.Interval = DefaultRunnerInterval
	}
	if c.Runner.Jitter == 0 {
		c.Runner.Jitter = DefaultRunnerJitter
	}
}

// validate ensures all required fields are present and well-formed.
// Defaults have already been applied, so checking for zero values
// here would mean "the user explicitly set this to zero", which we
// treat as a validation error where it matters.
func (c *Config) validate() error {
	if c.Readeck.BaseURL == "" {
		return errors.New("readeck.base_url is required")
	}
	if c.Readeck.APIToken == "" {
		return errors.New("readeck.api_token is required")
	}
	if c.Readeck.PageSize < 1 {
		return fmt.Errorf("readeck.page_size must be >= 1, got %d", c.Readeck.PageSize)
	}
	if c.Readeck.Timeout < 0 {
		return fmt.Errorf("readeck.timeout must be >= 0, got %v", c.Readeck.Timeout)
	}

	if c.LLM.BaseURL == "" {
		return errors.New("llm.base_url is required")
	}
	if c.LLM.APIKey == "" {
		return errors.New("llm.api_key is required")
	}
	if c.LLM.Model == "" {
		return errors.New("llm.model is required")
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature must be in [0, 2], got %v", c.LLM.Temperature)
	}
	if c.LLM.MaxInputChars < 1 {
		return fmt.Errorf("llm.max_input_chars must be >= 1, got %d", c.LLM.MaxInputChars)
	}

	if c.State.DBPath == "" {
		return errors.New("state.db_path is required")
	}

	if c.Classifier.MaxLabelsPerBookmark < 1 {
		return fmt.Errorf("classifier.max_labels_per_bookmark must be >= 1, got %d",
			c.Classifier.MaxLabelsPerBookmark)
	}
	if c.Classifier.MinConfidence < 0 || c.Classifier.MinConfidence > 1 {
		return fmt.Errorf("classifier.min_confidence must be in [0, 1], got %v",
			c.Classifier.MinConfidence)
	}

	if c.Runner.Interval <= 0 {
		return fmt.Errorf("runner.interval must be > 0, got %v", c.Runner.Interval)
	}
	if c.Runner.Jitter < 0 {
		return fmt.Errorf("runner.jitter must be >= 0, got %v", c.Runner.Jitter)
	}

	for i, g := range c.Collections.Groups {
		if g.Name == "" {
			return fmt.Errorf("collections.groups[%d].name is required", i)
		}
		if len(g.Labels) == 0 {
			return fmt.Errorf("collections.groups[%d].labels must contain at least one label", i)
		}
	}

	return nil
}
