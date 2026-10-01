// Package timelayout holds the store timestamp layout shared by internal/db and
// internal/search. It imports only the standard library.
package timelayout

import "time"

// Layout is fixed-width RFC 3339, so lexicographic and chronological order match.
// Stored timestamps are UTC.
const Layout = "2006-01-02T15:04:05.000000000Z07:00"

// Format renders t in Layout (UTC).
func Format(t time.Time) string {
	return t.UTC().Format(Layout)
}
