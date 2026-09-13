// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the config validate subcommand integration.
//
// These tests exercise the full path from CLI parsing through config
// loading, verifying that:
//   - valid configs are accepted with exit 0
//   - missing required fields are rejected with a clear message
//   - the loaded config is plumbed all the way to the Run() method

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// validConfigYAML is the smallest valid config used by validate tests.
const validateTestValidYAML = `
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

func TestConfigValidate_AcceptsValidFile(t *testing.T) {
	path := writeConfigTmp(t, validateTestValidYAML)

	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse([]string{"--config", path, "config", "validate"})
	if err != nil {
		t.Fatalf("kong.Parse: %v", err)
	}

	if err := runValidate(ctx, &cli); err != nil {
		t.Errorf("runValidate: unexpected error: %v", err)
	}
	if ctx.Command() != "config validate" {
		t.Errorf("Command: got %q, want %q", ctx.Command(), "config validate")
	}
}

func TestConfigValidate_RejectsMissingToken(t *testing.T) {
	path := writeConfigTmp(t, `
readeck:
  base_url: "https://readeck.example.com"

llm:
  base_url: "https://api.example.com/v1"
  api_key: "key"
  model: "model"

state:
  db_path: "/tmp/x.db"
`)

	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse([]string{"--config", path, "config", "validate"})
	if err != nil {
		t.Fatalf("kong.Parse: %v", err)
	}

	err = runValidate(ctx, &cli)
	if err == nil {
		t.Fatalf("runValidate: expected error for missing api_token, got nil")
	}
	if !strings.Contains(err.Error(), "api_token") {
		t.Errorf("error should mention api_token, got: %v", err)
	}
}

func TestConfigValidate_RejectsMissingFile(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse([]string{
		"--config", filepath.Join(t.TempDir(), "missing.yaml"),
		"config", "validate",
	})
	if err != nil {
		t.Fatalf("kong.Parse: %v", err)
	}

	err = runValidate(ctx, &cli)
	if err == nil {
		t.Fatalf("runValidate: expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "missing.yaml") {
		t.Errorf("error should mention the file path, got: %v", err)
	}
}

func TestConfigValidate_LoadsDefaultPath(t *testing.T) {
	// When --config is omitted, the default "readeckorator.yaml" is used.
	// We can't easily test that without polluting cwd, so we just
	// verify that the default flag value is "readeckorator.yaml".
	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	_, err = parser.Parse([]string{"config", "validate"})
	if err != nil {
		t.Fatalf("kong.Parse: %v", err)
	}
	if cli.Config != "readeckorator.yaml" {
		t.Errorf("Config default: got %q, want %q", cli.Config, "readeckorator.yaml")
	}
}

func writeConfigTmp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "readeckorator.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeConfigTmp: %v", err)
	}
	return path
}
