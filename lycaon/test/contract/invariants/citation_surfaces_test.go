package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/forbiddencalls"
	"github.com/lycaon/lycaon/test/contract/invariants/invariantstest"
)

var citationSurfaceTree = map[string]string{
	"lycaon/internal/evidence/resolve.go": "package evidence\n\nfunc Resolve() {}\nfunc ResolveObservation() {}\n",
	"lycaon/internal/citationmd/parse.go": "package citationmd\n\nfunc Parse() {}\n",
	"lycaon/internal/guidance/evaluate_worker_citations.go": "package guidance\n\n" +
		"import \"github.com/lycaon/lycaon/internal/evidence\"\n\n" +
		"func eval() { evidence.Resolve(); evidence.ResolveObservation() }\n",
	"lycaon/internal/guidance/closeout_draft.go": "package guidance\n\n" +
		"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
		"func parse() { citationmd.Parse() }\n",
	"lycaon/internal/llm/curator_impl.go": "package llm\n\n" +
		"import \"github.com/lycaon/lycaon/internal/evidence\"\n\n" +
		"func curate() { evidence.Resolve() }\n",
	"lycaon/internal/guidance/closeout_present_markdown.go": "package guidance\n\n" +
		"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
		"func present() { citationmd.Parse() }\n",
	"lycaon/internal/coordinator/guard/finish_guard.go": "package guard\n\n" +
		"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
		"func guard() { citationmd.Parse() }\n",
	"lycaon/internal/coordinator/promptloop/ledger_closeout_emit.go": "package promptloop\n\n" +
		"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
		"func emit() { citationmd.Parse() }\n",
	"lycaon/internal/coordinator/promptloop/provisional_turn.go": "package promptloop\n\n" +
		"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
		"func project() { citationmd.Parse() }\n",
	"lycaon/internal/session/policyfacts/reproduced_source.go": "package policyfacts\n\n" +
		"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
		"func facts() { citationmd.Parse() }\n",
	"lycaon/cmd/stub/main.go": "package main\n\nfunc main() {}\n",
	"lycaon/pkg/stub/stub.go": "package stub\n",
}

// TestCitationSurfacesInvariantFailClosed is the standard fixture self-test for
// the GB10 I1 static table-row invariant: clean, seeded violation, renamed
// anchor, stale exemption, empty selection, and broken source.
func TestCitationSurfacesInvariantFailClosed(t *testing.T) {
	t.Parallel()
	rule := invariantstest.Rule(t, forbiddencalls.Family, "citation-resolution-callers-are-registered-surfaces")

	for name, tc := range map[string]struct {
		files map[string]string
		want  invariantstest.Outcome
	}{
		"clean": {
			files: citationSurfaceTree,
			want:  invariantstest.Passes,
		},
		"seeded violation Resolve": {
			files: invariantstest.With(citationSurfaceTree, map[string]string{
				"lycaon/internal/unregistered/surface.go": "package unregistered\n\n" +
					"import ev \"github.com/lycaon/lycaon/internal/evidence\"\n\n" +
					"func bypass() { ev.Resolve() }\n",
			}),
			want: invariantstest.Violates,
		},
		"seeded violation ResolveObservation": {
			files: invariantstest.With(citationSurfaceTree, map[string]string{
				"lycaon/internal/unregistered/obs.go": "package unregistered\n\n" +
					"import \"github.com/lycaon/lycaon/internal/evidence\"\n\n" +
					"func bypass() { evidence.ResolveObservation() }\n",
			}),
			want: invariantstest.Violates,
		},
		"seeded violation citationmd.Parse": {
			files: invariantstest.With(citationSurfaceTree, map[string]string{
				"lycaon/internal/unregistered/parse.go": "package unregistered\n\n" +
					"import \"github.com/lycaon/lycaon/internal/citationmd\"\n\n" +
					"func bypass() { citationmd.Parse() }\n",
			}),
			want: invariantstest.Violates,
		},
		"renamed anchor": {
			files: invariantstest.With(citationSurfaceTree, map[string]string{
				"lycaon/internal/evidence/resolve.go": "package evidence\n\nfunc ResolveRenamed() {}\nfunc ResolveObservation() {}\n",
			}),
			want: invariantstest.MissesAnchor,
		},
		"stale exemption": {
			files: invariantstest.With(citationSurfaceTree, map[string]string{
				"lycaon/internal/llm/curator_impl.go": "package llm\n\nfunc curate() {}\n",
			}),
			want: invariantstest.StaleExemption,
		},
		"empty selection": {
			files: nil,
			want:  invariantstest.Fails,
		},
		"broken source": {
			files: invariantstest.With(citationSurfaceTree, map[string]string{
				"lycaon/internal/unregistered/broken.go": "package unregistered\n\nfunc (\n",
			}),
			want: invariantstest.Fails,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			invariantstest.Expect(t, invariants.Evaluate(invariantstest.Fixture(t, tc.files), rule), tc.want, 1)
		})
	}
}
