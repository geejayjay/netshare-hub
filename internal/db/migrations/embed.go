// Package migrations embeds the SQL migration files into the binary.
// Add a new numbered .sql file here to change the schema; it runs at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
