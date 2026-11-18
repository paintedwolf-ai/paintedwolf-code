package app

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
)

// These recoveries can launch turns and require the completed service graph.
var requiredServePhaseRecovery = map[string]string{
	"workflow-verdicts":       "a recovered verdict can auto-advance a workflow",
	"workflow-review-repairs": "a replayed repair can pause a run and hold its workers",
	"prompt-submissions":      "a recovered submission launches a coordinator turn",
	"worker-merge-applies":    "resuming a merge can close out worker work",
}

// The built registry holds both entry shapes and keeps turn-launching
// recovery in the serve phase. Running both phases again on the settled store
// reports every entry.
func TestBuiltRecoveryRegistryShape(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "Build failed", err)
	t.Cleanup(func() { _ = app.Close() })

	kinds := map[bootrecovery.Kind]int{}
	phase := map[string]bootrecovery.Phase{}
	for _, p := range []bootrecovery.Phase{bootrecovery.PhaseBuild, bootrecovery.PhaseServe} {
		report, err := app.recovery.Run(t.Context(), p)
		testutil.FailErr(t, "run recovery phase "+string(p), err)
		testutil.FailErr(t, "settle recovery phase "+string(p), report.Err())
		for _, outcome := range report.Outcomes {
			kinds[outcome.Kind]++
			phase[outcome.Name] = outcome.Phase
		}
	}

	t.Run("both entry kinds", func(t *testing.T) {
		if kinds[bootrecovery.KindJournal] == 0 || kinds[bootrecovery.KindReconcile] == 0 {
			t.Fatalf("entry kinds = %v, want journal and reconcile entries", kinds)
		}
	})
	t.Run("turn-launching recovery in the serve phase", func(t *testing.T) {
		for name, why := range requiredServePhaseRecovery {
			got, registered := phase[name]
			if !registered {
				t.Fatalf("recovery entry %q is not registered; %s", name, why)
			}
			if got != bootrecovery.PhaseServe {
				t.Fatalf("recovery entry %q runs in %s, want %s: %s", name, got, bootrecovery.PhaseServe, why)
			}
		}
	})
}
