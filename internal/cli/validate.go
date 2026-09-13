// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"github.com/alecthomas/kong"

	"github.com/inful/readeckorator/internal/config"
)

// runValidate loads the config file pointed to by cli.Config and
// returns any error from load, parse, defaults, or validation.
//
// This is the small adapter that lets the `config validate` subcommand
// report config errors to the user with a useful exit code, while
// leaving the rest of the CLI unaware of config internals.
func runValidate(_ *kong.Context, c *CLI) error {
	_, err := config.Load(c.Config)
	return err
}
