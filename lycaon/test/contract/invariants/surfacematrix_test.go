package invariants_test

import (
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/surfacematrix"
)

var surfaceTree = map[string]string{
	"lycaon/internal/coordinator/surface/surface.go": "package surface\n\nfunc CompileToolPlan() {}\n",
}

// The matrix reads the compiled stock surfaces; the family fails on each
// kind of drift.
func TestSurfaceMatrixFailsClosed(t *testing.T) {
	t.Parallel()
	routing := surfacematrix.Surfaces("routing", []surfacematrix.SurfaceRow{{ID: "implement_routing", Requires: []string{"task"}, Forbids: []string{"delegate_dispatch"}}})
	seeded := surfacematrix.Surfaces("seeded", []surfacematrix.SurfaceRow{{ID: "implement_routing", Requires: []string{"no_such_tool"}}})
	exempted := routing
	exempted.Exempt = []invariants.Exemption{{Key: "implement_routing", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  outcome
	}{
		"clean":            {rule: routing, files: surfaceTree, want: passes},
		"seeded violation": {rule: seeded, files: surfaceTree, want: violates},
		"renamed anchor": {rule: routing, files: map[string]string{
			"lycaon/internal/coordinator/surface/surface.go": "package surface\n\nfunc Compile() {}\n",
		}, want: missesAnchor},
		"stale exemption": {rule: exempted, files: surfaceTree, want: staleExemption},
		"empty corpus":    {rule: routing, files: nil, want: fails},
		"unparsable file": {rule: routing, files: with(surfaceTree, map[string]string{
			"lycaon/internal/coordinator/surface/broken.go": "package surface\n\nfunc broken( {\n",
		}), want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), tc.rule), tc.want, 1)
		})
	}
}
