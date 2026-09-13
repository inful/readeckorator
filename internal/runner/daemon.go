// SPDX-License-Identifier: GPL-3.0-or-later

package runner

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Daemon runs the classifier on a fixed interval until ctx is
// cancelled (typically by SIGINT/SIGTERM).
//
// Each tick:
//   - if ReconcileOnStart, run the collections reconciler
//   - run one classification pass with the configured page count
type Daemon struct {
	app     *AppContext
	logger  *slog.Logger
	every   time.Duration
	jitter  time.Duration
	pages   int
}

// NewDaemon constructs a Daemon. every must be > 0; jitter is
// random ± its value to avoid thundering herd on shared infra.
func NewDaemon(app *AppContext, logger *slog.Logger, every, jitter time.Duration, pages int) *Daemon {
	if every <= 0 {
		every = 5 * time.Minute
	}
	if jitter < 0 {
		jitter = 0
	}
	return &Daemon{app: app, logger: logger, every: every, jitter: jitter, pages: pages}
}

// Run blocks until ctx is cancelled. Returns ctx.Err() on
// shutdown. Per-pass errors are logged but do not stop the loop.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Info("daemon starting",
		slog.Duration("interval", d.every),
		slog.Duration("jitter", d.jitter),
	)

	// Run once immediately, then on every tick.
	d.tick(ctx)

	t := time.NewTimer(d.nextDelay())
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("daemon stopping", slog.String("reason", ctx.Err().Error()))
			return nil
		case <-t.C:
			d.tick(ctx)
			t.Reset(d.nextDelay())
		}
	}
}

// tick runs one full pass, logging and absorbing errors.
func (d *Daemon) tick(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		return
	}
	if _, err := d.app.Once(ctx, d.logger, d.pages); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		d.logger.Error("pass failed", slog.String("err", err.Error()))
	}
}

// nextDelay returns the duration to wait before the next tick,
// randomised by ± jitter so multiple daemons don't synchronise.
func (d *Daemon) nextDelay() time.Duration {
	//nolint:gosec // G404: jitter is not security-sensitive.
	delta := time.Duration(rand.Int64N(int64(d.jitter)*2+1)) - d.jitter
	delay := d.every + delta
	if delay < 0 {
		delay = 0
	}
	return delay
}
