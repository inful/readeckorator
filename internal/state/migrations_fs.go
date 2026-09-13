// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS
