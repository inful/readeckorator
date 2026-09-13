// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the kong-based CLI parser.
//
// These tests are the contract that drives the CLI struct shape. They
// verify that the global flags, defaults, enum validation, and
// subcommand routing all behave as documented in README.md.

package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
)

// parseCLI runs kong against the provided argv and returns the parsed
// CLI struct plus the kong context (so tests can inspect ctx.Command()).
func parseCLI(t *testing.T, argv ...string) (*CLI, *kong.Context) {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	ctx, err := parser.Parse(argv)
	if err != nil {
		t.Fatalf("kong.Parse(%v): %v", argv, err)
	}
	return &cli, ctx
}

// expectParseError fails the test unless kong rejects argv. It returns
// the parse error so callers that want to assert on the message can
// capture it; callers that don't can simply discard the return value.
func expectParseError(t *testing.T, argv ...string) error {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli, kong.Exit(func(int) {}))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	_, err = parser.Parse(argv)
	if err == nil {
		t.Fatalf("kong.Parse(%v): expected error, got nil", argv)
	}
	return err
}

func TestGlobalFlags_Defaults(t *testing.T) {
	cli, _ := parseCLI(t, "run")
	if got, want := cli.Config, "readeckorator.yaml"; got != want {
		t.Errorf("Config: got %q, want %q", got, want)
	}
	if got, want := cli.LogLevel, "info"; got != want {
		t.Errorf("LogLevel: got %q, want %q", got, want)
	}
	if cli.DryRun {
		t.Errorf("DryRun: got true, want false")
	}
}

func TestGlobalFlags_Explicit(t *testing.T) {
	cli, _ := parseCLI(t,
		"--config", "/etc/readeck.yaml",
		"--log-level", "debug",
		"--dry-run",
		"run",
	)
	if got, want := cli.Config, "/etc/readeck.yaml"; got != want {
		t.Errorf("Config: got %q, want %q", got, want)
	}
	if got, want := cli.LogLevel, "debug"; got != want {
		t.Errorf("LogLevel: got %q, want %q", got, want)
	}
	if !cli.DryRun {
		t.Errorf("DryRun: got false, want true")
	}
}

func TestGlobalFlags_InvalidLogLevelRejected(t *testing.T) {
	err := expectParseError(t, "--log-level", "verbose", "run")
	if !strings.Contains(err.Error(), "log-level") {
		t.Errorf("error should mention log-level, got: %v", err)
	}
}

func TestDefaultSubcommandIsRun(t *testing.T) {
	cli, ctx := parseCLI(t)
	if got, want := ctx.Command(), "run"; got != want {
		t.Errorf("Command: got %q, want %q", got, want)
	}
	// Pages default is 10 per the struct tag.
	if got, want := cli.Run.Pages, 10; got != want {
		t.Errorf("Run.Pages initial: got %d, want %d", got, want)
	}
}

func TestRunCmd_PagesFlag(t *testing.T) {
	cli, _ := parseCLI(t, "run", "--pages", "42")
	if got, want := cli.Run.Pages, 42; got != want {
		t.Errorf("Run.Pages: got %d, want %d", got, want)
	}
}

func TestServeCmd_IntervalFlag(t *testing.T) {
	cli, _ := parseCLI(t, "serve", "--interval", "10m")
	if got, want := cli.Serve.Interval, 10*time.Minute; got != want {
		t.Errorf("Serve.Interval: got %v, want %v", got, want)
	}
}

func TestClassifyCmd_IDArg(t *testing.T) {
	cli, _ := parseCLI(t, "classify", "abc123")
	if got, want := cli.Classify.ID, "abc123"; got != want {
		t.Errorf("Classify.ID: got %q, want %q", got, want)
	}
}

func TestClassifyCmd_RequiresID(t *testing.T) {
	_ = expectParseError(t, "classify")
}

func TestBackfillCmd_ForceFlag(t *testing.T) {
	cli, _ := parseCLI(t, "backfill", "--force")
	if !cli.Backfill.Force {
		t.Errorf("Backfill.Force: got false, want true")
	}
}

func TestLabelsCmd_ListSubcommand(t *testing.T) {
	_, ctx := parseCLI(t, "labels", "list")
	if got, want := ctx.Command(), "labels list"; got != want {
		t.Errorf("Command: got %q, want %q", got, want)
	}
}

func TestLabelsCmd_PruneSubcommand(t *testing.T) {
	cli, _ := parseCLI(t, "labels", "prune", "--prune-dry-run")
	if !cli.Labels.Prune.PruneDryRun {
		t.Errorf("Labels.Prune.PruneDryRun: got false, want true")
	}
}

func TestCollectionsCmd_SyncSubcommand(t *testing.T) {
	_, ctx := parseCLI(t, "collections", "sync")
	if got, want := ctx.Command(), "collections sync"; got != want {
		t.Errorf("Command: got %q, want %q", got, want)
	}
}

func TestConfigCmd_ValidateSubcommand(t *testing.T) {
	_, ctx := parseCLI(t, "config", "validate")
	if got, want := ctx.Command(), "config validate"; got != want {
		t.Errorf("Command: got %q, want %q", got, want)
	}
}

func TestVersionCmd(t *testing.T) {
	_, ctx := parseCLI(t, "version")
	if got, want := ctx.Command(), "version"; got != want {
		t.Errorf("Command: got %q, want %q", got, want)
	}
}

func TestUnknownSubcommandIsRejected(t *testing.T) {
	_ = expectParseError(t, "bogus")
}
