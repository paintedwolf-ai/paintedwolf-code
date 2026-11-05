package command_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestReadOnlyNativeInvestigationSurvivesPolicyMutationBarriers(t *testing.T) {
	for _, code := range []string{"POLICY_CONTEXT_CHANGED", "POLICY_CONTEXT_UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			invoked, reads, mutations := 0, 0, 0
			f := policyOperandFixture(t, "write", &invoked)
			ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
				for _, target := range targets {
					if target.Mutation {
						mutations++
						return &toolrejection.ToolReject{Code: code}
					}
					reads++
				}
				return nil
			})
			raw, err := f.Call(t, ctx, "pwd; tee nested/target")
			testutil.FailErr(t, "run read-only investigation before mutation barrier", err)
			var result struct {
				Stdout string `json:"stdout"`
				Status int    `json:"status"`
			}
			testutil.FailErr(t, "decode policy classification outcome", json.Unmarshal([]byte(raw), &result))
			if result.Stdout != f.Dir+"\n" || result.Status == 0 || invoked != 0 || reads == 0 || mutations != 1 {
				t.Fatalf("policy classification outcome=%s reads=%d mutations=%d mutation dispatches=%d", raw, reads, mutations, invoked)
			}
		})
	}
}
