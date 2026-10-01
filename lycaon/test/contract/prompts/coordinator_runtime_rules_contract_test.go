package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorRuntimeGuardCodesRegistered(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, code := range []string{
		"COORDINATOR_ORCHESTRATE_WRITE_DENIED",
		"COORDINATOR_READ_OUTSIDE_SCOPE",
		"COORDINATOR_BATCH_ALREADY_CLOSED",
		"COORDINATOR_BATCH_WRONG_PHASE",
		"COORDINATOR_SYNTHESIS_WRAPUP_ONLY",
		"PROGRESS_MISSING",
		"PROGRESS_SYNTHESIS_RECONCILE_ONLY",
	} {
		if _, ok := cfg.HintCodes[code]; !ok {
			t.Fatalf("missing hint code %q", code)
		}
	}
}

func TestBundledAgentToolProfilesRuntimeRules(t *testing.T) {
	t.Parallel()
	rules, err := toolpolicy.LoadProfileRuntimeRules()
	contractcheck.FailErr(t, "load agent-tool-profiles runtime rules", err)
	hints, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	fmt := guidance.NewStaticRejectFormatter(hints)
	sess := &api.Session{ID: "s1", AgentType: "coordinator"}

	err = rules.EvaluateCoordinator(sess, "task", map[string]any{"agent_type": "repo-researcher"}, fmt)
	if err != nil {
		t.Fatalf("repo-researcher task should be allowed for coordinator: %v", err)
	}
}
