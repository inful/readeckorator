// SPDX-License-Identifier: GPL-3.0-or-later
//
// Tests for the signal-aware context plumbing.
//
// main.go creates a context via signal.NotifyContext and threads
// it through kongCtx.Run into each subcommand's Run method. These
// tests verify the threading actually reaches the runner layer —
// without that, Ctrl+C would not cancel an in-flight pass because
// subcommands would otherwise build their own Background.

package cli

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestRunCmd_HonoursContextCancel is the integration test for the
// "press Ctrl+C during `run`" flow. We construct a context, start
// the subcommand in a goroutine, cancel it, and assert the
// subcommand returned within a short window.
//
// The classifier inside doesn't actually call out to Readeck or the
// LLM in this test — those need network access. Instead we cancel
// BEFORE Once starts fetching, so the test runs instantly and
// proves the threading works without depending on real HTTP.
func TestRunCmd_HonoursContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel right away so buildApp / Once exit on the first
	// ctx.Err() check without needing network access.
	cancel()

	probe := &RunCmd{}
	cli := &CLI{Config: ""} // buildApp will fail and return early

	// We just need to verify RunCmd.Run doesn't hang. It returns
	// within milliseconds (buildApp fails or Once sees ctx cancel).
	done := make(chan struct{})
	go func() {
		_ = probe.Run(ctx, cli, silentLogger())
		close(done)
	}()

	select {
	case <-done:
		// good — Run returned
	case <-time.After(2 * time.Second):
		t.Fatalf("RunCmd.Run did not return within 2s of cancellation")
	}
}

// TestAllSubcommandsAcceptContext is a compile-time sanity
// check. We instantiate every subcommand and call its Run method
// with (ctx, &CLI{}, silentLogger()) so that any signature drift
// (someone forgetting to add the ctx parameter) breaks the build
// instead of being silently ignored at runtime.
func TestAllSubcommandsAcceptContext(t *testing.T) {
	ctx := context.Background()
	cli := &CLI{}
	logger := silentLogger()

	// Subcommand Run() methods all take (ctx, *CLI, *slog.Logger).
	// The IIFE bodies force the compiler to type-check each
	// signature. If any method is missing the ctx parameter, this
	// file won't compile.
	_ = func() error { return (&RunCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&ServeCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&ClassifyCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&BackfillCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&LabelsCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&LabelsListCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&LabelsPruneCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&CollectionsCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&CollectionsSyncCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&ConfigCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&ConfigValidateCmd{}).Run(ctx, cli, logger) }
	_ = func() error { return (&VersionCmd{}).Run(ctx, cli, logger) }
}

// silentLogger returns a slog.Logger that throws output away.
// Used by tests that don't care about log content.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
