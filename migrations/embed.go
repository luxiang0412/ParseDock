// Package migrations embeds the SQL schema so the Coordinator binary is
// self-contained. The .sql files stay at the repository root (per the repo
// layout) and are the single source of truth.
package migrations

import "embed"

// FS holds the ordered *.sql migration files.
//
//go:embed *.sql
var FS embed.FS
