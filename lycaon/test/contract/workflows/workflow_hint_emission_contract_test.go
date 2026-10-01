package contract

import (
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestStaticWorkflowHintCodesRegisteredInYAML holds the run-context hints and
// the inject:active_workflow units to one set: every code the inject can list
// has copy, and every unit declaring that channel has a producer.
func TestStaticWorkflowHintCodesRegisteredInYAML(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	cases := []struct {
		name  string
		ctx   api.CoordinatorRunContext
		draft bool
	}{
		{
			name: "gate_unmet",
			ctx:  api.CoordinatorRunContext{FailedLeaves: []string{"human_approval"}},
		},
		{
			name: "feedback_pending",
			ctx:  api.CoordinatorRunContext{PendingFeedback: &api.PendingFeedback{PhaseID: "clarify", Prompt: "Q?"}},
		},
		{
			name: "session_compose_required",
			ctx:  api.CoordinatorRunContext{WorkflowID: "hotfix-session"},
		},
	}

	produced := map[string]bool{}
	for _, tc := range cases {
		for _, code := range surface.StaticWorkflowHintCodes(tc.ctx, tc.draft) {
			entry, ok := cfg.HintCodes[code]
			if !ok {
				t.Fatalf("%s: hint code %q not in hint registry", tc.name, code)
			}
			if !hintEntryHasProse(entry) {
				t.Fatalf("%s: hint %q missing what/message or instead", tc.name, code)
			}
			produced[code] = true
		}
	}

	var unproduced []string
	for code, entry := range cfg.HintCodes {
		anchorID, ok := guidance.InjectAnchor(entry.Emit)
		if !ok || anchorID != "inject.active_workflow" {
			continue
		}
		if !produced[code] {
			unproduced = append(unproduced, code)
		}
	}
	sort.Strings(unproduced)
	if len(unproduced) > 0 {
		t.Fatalf("inject:active_workflow units no run context produces: %v", unproduced)
	}
}
