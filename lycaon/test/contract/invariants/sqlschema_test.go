package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/sqlschema"
)

const peopleSchema = `PRAGMA foreign_keys = ON;
CREATE TABLE people (id TEXT PRIMARY KEY) STRICT;
CREATE TABLE notes (
    id TEXT PRIMARY KEY,
    author_person_id TEXT NOT NULL REFERENCES people(id)
) STRICT;
CREATE INDEX idx_notes_author ON notes(author_person_id);
`

// The family fails on each kind of drift, shown on rows over the live
// schema.
func TestSQLSchemaFailsClosed(t *testing.T) {
	t.Parallel()
	columns := rule(t, sqlschema.Family, "person-columns-are-keyed-constrained-and-indexed")
	actors := bare(rule(t, sqlschema.Family, "person-actor-vocabulary-names-the-person"))
	actors.Exempt = []invariants.Exemption{{Key: "retired_table", Reason: "fixture"}}
	tree := func(schema string) map[string]string {
		return map[string]string{invariants.SchemaPath: schema}
	}
	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  outcome
	}{
		"clean": {rule: columns, files: tree(peopleSchema), want: passes},
		"seeded violation": {rule: columns, files: tree(`CREATE TABLE people (id TEXT PRIMARY KEY) STRICT;
CREATE TABLE notes (id TEXT PRIMARY KEY, author_person_id TEXT NOT NULL REFERENCES people(id)) STRICT;
`), want: violates},
		"renamed anchor":  {rule: columns, files: map[string]string{"lycaon/internal/db/store.sql": peopleSchema}, want: missesAnchor},
		"stale exemption": {rule: actors, files: tree(peopleSchema), want: staleExemption},
		"empty corpus":    {rule: columns, files: tree("PRAGMA foreign_keys = ON;\n"), want: fails},
		"unparsable file": {rule: columns, files: tree("CREATE TABLE people (id TEXT PRIMARY KEY\n"), want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), tc.rule), tc.want, 1)
		})
	}
}
