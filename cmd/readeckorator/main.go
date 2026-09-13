// SPDX-License-Identifier: GPL-3.0-or-later

// Command readeckorator classifies Readeck bookmarks using an
// OpenAI-compatible LLM, applying labels (additive only) and
// reconciling collections according to the configured label groups.
//
// Subcommands:
//   run         one-shot classification pass (default)
//   serve       long-running poller
//   classify    classify one specific bookmark by ID
//   backfill    classify every unprocessed bookmark
//   labels      list or prune labels
//   collections sync Readeck collections from config
//   config      validate the config file
//   version     print version info
//
// See README.md and docs/ for full documentation.

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/inful/readeckorator/internal/cli"
)

// version is set at build time via -ldflags "-X main.version=v1.2.3".
var version = "dev"

// appName is the binary name used in help text and logs.
const appName = "readeckorator"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr *os.File) int {
	var c cli.CLI

	// Build the signal-aware context BEFORE creating the kong
	// parser, because the binding below captures it.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	parser, err := kong.New(&c,
		kong.Name(appName),
		kong.Description("Readeck bookmark classifier powered by an OpenAI-compatible LLM."),
		kong.Exit(func(int) {}), // never auto-exit; we handle exit codes ourselves
		// Bind the signal-aware context so subcommand Run(ctx, ...)
		// methods receive it. Without this binding, kong can't
		// match the context.Context parameter.
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		// If stderr is itself broken, there's nothing useful we can do.
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 2
	}

	kongCtx, err := parser.Parse(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", appName, err)
		return 2
	}

	logger := newLogger(c.LogLevel, stderr)

	if err := dispatch(kongCtx, &c, logger); err != nil {
		// Cancellation isn't a "command failed" — it's a clean exit
		// triggered by the user. Log at info level and return 0.
		if errors.Is(err, context.Canceled) {
			logger.Info("command cancelled")
			return 0
		}
		logger.Error("command failed", slog.String("err", err.Error()))
		return 1
	}
	return 0
}

// dispatch runs the selected subcommand. Each subcommand struct has a
// Run(ctx, *CLI, *slog.Logger) method; kong calls it via kongCtx.Run.
// The context is injected via the kong.BindTo binding registered in run().
func dispatch(kongCtx *kong.Context, c *cli.CLI, logger *slog.Logger) error {
	logger.Info("starting",
		slog.String("command", kongCtx.Command()),
		slog.String("version", version),
		slog.Bool("dry_run", c.DryRun),
		slog.String("config", c.Config),
	)
	return kongCtx.Run(c, logger)
}

// newLogger builds a slog.Logger whose level is controlled by the
// --log-level flag. Output goes to stderr so stdout stays clean for
// future machine-readable output.
func newLogger(level string, w *os.File) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
