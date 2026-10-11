package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandApprovalDependsOnAppliedContainmentNotCommandText(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "load approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := t.TempDir()

	commands := []string{
		"cat /etc/hosts",
		"cp source /etc/hosts",
		"env sh -c 'cp source /etc/hosts'",
		"tool-with-a-new-name --write-anywhere",
	}
	for _, command := range commands {
		for _, tc := range []struct {
			name      string
			contained hitl.Contained
			wantAsk   bool
		}{
			{
				name:      "jailed proxy egress",
				contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}},
			},
			{
				name:      "jailed denied egress",
				contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDeny, Roots: []string{proj}},
			},
			{
				name:      "uncontained filesystem",
				contained: hitl.Contained{FSJailed: false, Egress: hitl.ContainedEgressProxy},
				wantAsk:   true,
			},
			{
				name:      "uncontained egress",
				contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{proj}},
				wantAsk:   true,
			},
		} {
			t.Run(tc.name+"/"+command, func(t *testing.T) {
				result, evalErr := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": command},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: tc.contained,
},
})
				testutil.FailErr(t, "evaluate command action", evalErr)
				if result.Denied || result.Required() != tc.wantAsk {
					t.Fatalf("result=%+v want required=%v", result, tc.wantAsk)
				}
			})
		}
	}
}
