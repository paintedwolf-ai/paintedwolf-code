// Package migrations holds the SQL migration files embedded into the binary.
// go:embed cannot traverse parent directories, so the FS lives beside the SQL.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS