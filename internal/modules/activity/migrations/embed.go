// Package migrations holds the activity database's schema. It is a separate
// file from the main database (activity.db), so heavy SMB activity never
// slows down or bloats the main one.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
