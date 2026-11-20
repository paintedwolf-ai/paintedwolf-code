// Package sqlschema holds the invariants over the host store: its live schema,
// executed from schema.sql, and the Go and SQL that query it.
package sqlschema

import (
	"slices"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// Family is the sql-schema rule table.
var Family = invariants.Family{Name: "sql-schema", Invariants: slices.Concat(queryRules, schemaRules)}
