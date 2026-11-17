package laws_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/laws"
	"github.com/lycaon/lycaon/test/contract/laws/specsync"
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

// The family fails on each kind of drift, shown on rows over the bundled
// OpenAPI document.
func TestSpecSyncFailsClosed(t *testing.T) {
	t.Parallel()
	operationIDs := rule(t, specsync.Family, "routes-have-operation-ids")
	exempted := operationIDs
	exempted.Exempt = []laws.Exemption{{Key: "listGadgets", Reason: "fixture"}}
	eventSchemas := rule(t, specsync.Family, "event-schemas-follow-the-topic-vocabulary")
	for name, tc := range map[string]struct {
		rule  laws.Rule
		files map[string]string
		want  outcome
	}{
		"clean": {rule: operationIDs, files: specTree, want: passes},
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
		}, want: violates},
		"renamed anchor":  {rule: operationIDs, files: map[string]string{"docs/api.yaml": fixtureSpec}, want: missesAnchor},
		"stale exemption": {rule: exempted, files: specTree, want: staleExemption},
		"empty corpus": {rule: eventSchemas, files: map[string]string{
			"docs/openapi/vocab/EventTopic.yaml": "values: []\n",
			"docs/schemas/events/envelope.json":  "{}\n",
		}, want: fails},
		"unparsable file": {rule: operationIDs, files: map[string]string{"docs/openapi.yaml": "paths: [unclosed\n"}, want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, laws.Evaluate(fixture(t, tc.files), tc.rule), tc.want, 1)
		})
	}
}

// A document that fails to load fails every rule that reads it, not only
// the first.
func TestFailedLoadFailsEveryReader(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, map[string]string{"docs/openapi.yaml": "paths: [unclosed\n"})
	for _, name := range []string{"routes-have-operation-ids", "route-security", "openapi-operation-ids"} {
		expect(t, laws.Evaluate(corpus, rule(t, specsync.Family, name)), fails, 0)
	}
}
