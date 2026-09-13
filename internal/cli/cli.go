// SPDX-License-Identifier: GPL-3.0-or-later
//
// Package cli defines the readeckorator command-line surface using
// alecthomas/kong. The CLI struct is the single source of truth for
// flags and subcommands; main.go wires it into kong.Parse and runs
// the resulting Run() method.
//
// Kong tag syntax reminders (v1.16):
//   - items separated by ","
//   - key='value' (single quotes for quoting)
//   - enum values separated by ","  (not pipe)
//   - flag name uses key "name", not the first bare token
//   - command marker is "cmd", default subcommand is "cmd,default='1'"

package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/inful/readeckorator/internal/classifier"
	"github.com/inful/readeckorator/internal/collections"
	"github.com/inful/readeckorator/internal/config"
	"github.com/inful/readeckorator/internal/runner"
)

// version is set at build time via -ldflags "-X github.com/inful/readeckorator/internal/cli.version=v1.2.3".
var version = "dev"

// CLI is the root command parsed by kong.
//
// Global flags are defined here; each subcommand is its own struct so
// that help text, validation, and Run() dispatch stay localised.
type CLI struct {
	// Path to the YAML config file. Required by Run/Serve/Backfill, but
	// not by Classify (which only needs an ID) or Version.
	Config string `kong:"name='config',default='readeckorator.yaml'"`

	// Logging verbosity. Must be one of debug|info|warn|error.
	LogLevel string `kong:"name='log-level',enum='debug,info,warn,error',default='info'"`

	// When true, no mutations are written back to Readeck or the local
	// state DB; the pipeline runs end-to-end but apply steps become no-ops.
	DryRun bool `kong:"name='dry-run'"`

	// Subcommands. Exactly one of these is selected per invocation.
	// "run" is the default when no subcommand is supplied.
	Run         RunCmd         `kong:"cmd,default='1'"`
	Serve       ServeCmd       `kong:"cmd"`
	Classify    ClassifyCmd    `kong:"cmd"`
	Backfill    BackfillCmd    `kong:"cmd"`
	Labels      LabelsCmd      `kong:"cmd"`
	Collections CollectionsCmd `kong:"cmd"`
	Cfg         ConfigCmd      `kong:"cmd,name='config'"`
	Version     VersionCmd     `kong:"cmd"`
}

// RunCmd performs a single classification pass over new bookmarks and exits.
type RunCmd struct {
	// Maximum number of bookmark pages to fetch from Readeck in this run.
	Pages int `kong:"name='pages',default='10'"`
}

// ServeCmd runs continuously, polling Readeck on a fixed interval.
type ServeCmd struct {
	// How often to poll Readeck for new bookmarks.
	Interval time.Duration `kong:"name='interval',default='5m'"`
	// Maximum random jitter added to each interval (to avoid thundering herd).
	Jitter time.Duration `kong:"name='jitter',default='30s'"`
}

// ClassifyCmd classifies one specific bookmark by its ID, ignoring the
// state DB (always processes the given ID).
type ClassifyCmd struct {
	// ID of the bookmark to classify.
	ID string `kong:"arg,required"`
}

// BackfillCmd classifies every bookmark that does not yet have an entry
// in the state DB (and optionally re-classifies everything).
type BackfillCmd struct {
	// When true, re-classify every bookmark, ignoring state DB entries.
	Force bool `kong:"name='force'"`
}

// LabelsCmd groups label-related subcommands.
type LabelsCmd struct {
	List  LabelsListCmd  `kong:"cmd"`
	Prune LabelsPruneCmd `kong:"cmd"`
}

// LabelsListCmd prints all labels known to Readeck and to the state DB.
type LabelsListCmd struct{}

// LabelsPruneCmd deletes labels that have zero bookmarks from Readeck.
type LabelsPruneCmd struct {
	// When true, print what would be deleted without deleting anything.
	// Field name is PruneDryRun so the auto-derived flag --prune-dry-run
	// avoids colliding with the global --dry-run flag.
	PruneDryRun bool `kong:""`
}

// CollectionsCmd groups collection-related subcommands.
type CollectionsCmd struct {
	Sync CollectionsSyncCmd `kong:"cmd"`
}

// CollectionsSyncCmd reconciles Readeck collections against the
// configured label groups.
type CollectionsSyncCmd struct{}

// ConfigCmd groups configuration-related subcommands.
type ConfigCmd struct {
	Validate ConfigValidateCmd `kong:"cmd"`
}

// ConfigValidateCmd loads the config and prints any validation errors.
type ConfigValidateCmd struct{}

// VersionCmd prints version information and exits.
type VersionCmd struct{}

// Run methods on each subcommand struct. These are stubs in phase 1;
// subsequent phases replace them with real implementations. They
// exist so kong can dispatch via kongCtx.Run() today.

func (r *RunCmd) Run(c *CLI, logger *slog.Logger) error {
	app, err := buildApp(c, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err = app.Once(ctx, logger, r.Pages)
	return err
}

func (s *ServeCmd) Run(c *CLI, logger *slog.Logger) error {
	app, err := buildApp(c, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	ctx, cancel := signalCtx(context.Background())
	defer cancel()

	d := runner.NewDaemon(app, logger, s.Interval, s.Jitter, 1)
	return d.Run(ctx)
}

func (k *ClassifyCmd) Run(c *CLI, logger *slog.Logger) error {
	app, err := buildApp(c, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	// Force-classify: clear any prior "processed" row so the
	// pipeline re-runs end to end on this bookmark.
	if err := app.Store.DeleteProcessed(context.Background(), k.ID); err != nil {
		logger.Warn("could not clear prior classification",
			slog.String("bookmark_id", k.ID),
			slog.String("err", err.Error()),
		)
	}

	result, err := app.Pipeline.Classify(context.Background(), k.ID)
	if err != nil {
		return err
	}
	logger.Info("classify complete",
		slog.String("bookmark_id", k.ID),
		slog.Bool("skipped", result.Skipped),
		slog.String("skip_reason", result.SkipReason),
		slog.Any("applied_labels", result.AppliedLabels),
		slog.Float64("confidence", result.Confidence),
	)
	return nil
}

func (b *BackfillCmd) Run(c *CLI, logger *slog.Logger) error {
	app, err := buildApp(c, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	if b.Force {
		logger.Warn("backfill --force: re-classifying every bookmark (this may take a while)")
		if err := app.Store.WipeProcessed(context.Background()); err != nil {
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = app.Once(ctx, logger, 100)
	return err
}

func (l *LabelsCmd) Run(c *CLI, logger *slog.Logger) error {
	// kongCtx.Run() lands here; the actual sub-subcommand (list/prune)
	// is dispatched by the embedded cmd struct via its own Run() method
	// when kong walks further into the tree.
	return nil
}

func (l *LabelsListCmd) Run(c *CLI, logger *slog.Logger) error {
	app, err := buildApp(c, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	stats, err := app.Store.ListLabels(context.Background())
	if err != nil {
		return err
	}
	for _, s := range stats {
		logger.Info("label",
			slog.String("name", s.Name),
			slog.Int("use_count", s.UseCount),
		)
	}
	return nil
}

func (l *LabelsPruneCmd) Run(c *CLI, logger *slog.Logger) error {
	// Read the Readeck inventory and find labels with count==0.
	app, err := buildApp(c, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	labels, err := app.Readeck.ListLabels(context.Background())
	if err != nil {
		return err
	}
	var toDelete []string
	for _, l := range labels {
		if l.Count == 0 {
			toDelete = append(toDelete, l.Name)
		}
	}
	if l.PruneDryRun {
		logger.Info("prune (dry run)",
			slog.Int("would_delete", len(toDelete)),
			slog.Any("labels", toDelete),
		)
		return nil
	}
	// Real delete not yet wired — see phase 10.
	logger.Warn("real prune not yet implemented; rerun with --prune-dry-run to see candidates",
		slog.Int("candidates", len(toDelete)),
	)
	return nil
}

func (c *CollectionsCmd) Run(_ *CLI, _ *slog.Logger) error {
	return nil
}

func (c *CollectionsSyncCmd) Run(cli *CLI, logger *slog.Logger) error {
	app, err := buildApp(cli, logger)
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	groups := make([]collections.Group, 0, len(app.Cfg.Collections.Groups))
	for _, g := range app.Cfg.Collections.Groups {
		groups = append(groups, collections.Group{Name: g.Name, Labels: g.Labels})
	}
	return app.Reconciler.Reconcile(context.Background(), groups)
}

func (c *ConfigCmd) Run(_ *CLI, _ *slog.Logger) error {
	return nil
}

func (c *ConfigValidateCmd) Run(cli *CLI, logger *slog.Logger) error {
	cfg, err := config.Load(cli.Config)
	if err != nil {
		return err
	}
	logger.Info("config validate: OK",
		slog.String("path", cli.Config),
		slog.String("readeck_base_url", cfg.Readeck.BaseURL),
		slog.String("llm_model", cfg.LLM.Model),
		slog.Int("collection_groups", len(cfg.Collections.Groups)),
	)
	return nil
}

func (v *VersionCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("version", slog.String("version", version))
	return nil
}

// buildApp constructs an AppContext from the CLI's --config
// flag. It logs the start line at INFO; errors short-circuit
// with a wrapped message.
func buildApp(c *CLI, logger *slog.Logger) (*runner.AppContext, error) {
	app, err := runner.NewApp(context.Background(), c.Config, logger)
	if err != nil {
		if errors.Is(err, classifier.ErrNoPipeline) {
			return nil, errors.New("app initialisation failed: check config")
		}
		return nil, err
	}
	return app, nil
}

// signalCtx returns a context that is cancelled on SIGINT or
// SIGTERM. Kept here (rather than in main.go) so each subcommand
// can wire it without depending on the os package directly.
func signalCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
