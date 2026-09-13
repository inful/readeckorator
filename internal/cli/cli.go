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
	"log/slog"
	"time"
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
	logger.Info("run: not yet implemented",
		slog.Int("pages", r.Pages),
	)
	return nil
}

func (s *ServeCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("serve: not yet implemented",
		slog.Duration("interval", s.Interval),
		slog.Duration("jitter", s.Jitter),
	)
	return nil
}

func (k *ClassifyCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("classify: not yet implemented",
		slog.String("id", k.ID),
	)
	return nil
}

func (b *BackfillCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("backfill: not yet implemented",
		slog.Bool("force", b.Force),
	)
	return nil
}

func (l *LabelsCmd) Run(c *CLI, logger *slog.Logger) error {
	// kongCtx.Run() lands here; the actual sub-subcommand (list/prune)
	// is dispatched by the embedded cmd struct via its own Run() method
	// when kong walks further into the tree.
	return nil
}

func (l *LabelsListCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("labels list: not yet implemented")
	return nil
}

func (l *LabelsPruneCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("labels prune: not yet implemented",
		slog.Bool("prune_dry_run", l.PruneDryRun),
	)
	return nil
}

func (c *CollectionsCmd) Run(_ *CLI, _ *slog.Logger) error {
	return nil
}

func (c *CollectionsSyncCmd) Run(_ *CLI, logger *slog.Logger) error {
	logger.Info("collections sync: not yet implemented")
	return nil
}

func (c *ConfigCmd) Run(_ *CLI, _ *slog.Logger) error {
	return nil
}

func (c *ConfigValidateCmd) Run(_ *CLI, logger *slog.Logger) error {
	logger.Info("config validate: not yet implemented")
	return nil
}

func (v *VersionCmd) Run(c *CLI, logger *slog.Logger) error {
	logger.Info("version", slog.String("version", version))
	return nil
}
