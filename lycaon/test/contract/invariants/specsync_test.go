package invariants_test

import (
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/specsync"
)

const fixtureSpec = `openapi: 3.1.0
paths:
  /v1/widgets:
    get:
      operationId: listWidgets
      responses:
        "200":
          description: ok
components:
  schemas:
    Widget:
      type: object
`

var specTree = map[string]string{"docs/openapi.yaml": fixtureSpec}

// The family invariantstest.Fails on each kind of drift, shown on rows over the bundled
// OpenAPI document.
func TestSpecSyncFailsClosed(t *testing.T) {
	t.Parallel()
	operationIDs := invariantstest.Rule(t, specsync.Family, "routes-have-operation-ids")
	exempted := operationIDs
	exempted.Exempt = []invariants.Exemption{{Key: "listGadgets", Reason: "fixture"}}
	eventSchemas := invariantstest.Rule(t, specsync.Family, "event-schemas-follow-the-topic-vocabulary")
	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  invariantstest.Outcome
	}{
		"clean": {rule: operationIDs, files: specTree, want: invariantstest.Passes},
		"seeded violation": {rule: operationIDs, files: map[string]string{
			"docs/openapi.yaml": `openapi: 3.1.0
paths:
  /v1/widgets:
    get:
      responses: {}
components:
  schemas:
    Widget: {type: object}
`,
		}, want: invariantstest.Violates},
		"renamed anchor":  {rule: operationIDs, files: map[string]string{"docs/api.yaml": fixtureSpec}, want: invariantstest.MissesAnchor},
		"stale exemption": {rule: exempted, files: specTree, want: invariantstest.StaleExemption},
		"empty corpus": {rule: eventSchemas, files: map[string]string{
			"docs/openapi/vocab/EventTopic.yaml": "values: []\n",
			"docs/schemas/events/envelope.json":  "{}\n",
		}, want: invariantstest.Fails},
		"unparsable file": {rule: operationIDs, files: map[string]string{"docs/openapi.yaml": "paths: [unclosed\n"}, want: invariantstest.Fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), tc.rule), tc.want, 1)
		})
	}
}

// A document that invariantstest.Fails to load invariantstest.Fails every rule that reads it, not only
// the first.
func TestFailedLoadFailsEveryReader(t *testing.T) {
	t.Parallel()
	corpus := invariantstest.Fixture(t, map[string]string{"docs/openapi.yaml": "paths: [unclosed\n"})
	for _, name := range []string{"routes-have-operation-ids", "route-security", "openapi-operation-ids"} {
		invariantstest.Expect(t, invariants.Evaluate(corpus, invariantstest.Rule(t, specsync.Family, name)), invariantstest.Fails, 0)
	}
}
