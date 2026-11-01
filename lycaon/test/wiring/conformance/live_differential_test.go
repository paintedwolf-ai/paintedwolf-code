//go:build referenceuserland

package conformance_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/commandconformance"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

// TestLiveReferenceDifferential runs the corpus in the verified reference images,
// fails when a stored snapshot no longer reproduces, and compares every program
// run through the command entry with the live reference results.
// ./task test:conformance:live builds and verifies the images first.
func TestLiveReferenceDifferential(t *testing.T) {
	cases := corpus(t)
	stored, err := commandconformance.LoadSnapshots()
	testutil.FailErr(t, "load reference snapshots", err)
	references, err := referenceuserland.NewRunner(t.Context())
	testutil.FailErr(t, "open the reference runner", err)
	t.Cleanup(func() { _ = references.Close() })
	runner := newConformanceRunner(t, gate.PostureLight, true)

	live, err := commandconformance.Capture(t.Context(), references, cases, runner.seed)
	testutil.FailErr(t, "run the corpus in the reference userlands", err)
	if problems := commandconformance.Reproduces(stored.Cases, live); len(problems) > 0 {
		t.Errorf("stored snapshots no longer reproduce in %s; run ./task conformance:capture:\n  %s",
			references.Architecture(), strings.Join(problems, "\n  "))
	}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			assertMatchesReference(t, c, runner.runCase(t, c), live[c.ID])
		})
	}
}
