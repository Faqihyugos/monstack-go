package migrations

import "embed"

// FS embeds all .sql migration files in this package directory.
//
//go:embed *.sql
var FS embed.FS
