// Package sqlschema holds the laws over the host store: its live schema,
// executed from schema.sql, and the Go and SQL that query it.
package sqlschema

import (
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// Family is the sql-schema rule table.
var Family = laws.Family{Name: "sql-schema", Rules: slices.Concat(queryRules, schemaRules)}
