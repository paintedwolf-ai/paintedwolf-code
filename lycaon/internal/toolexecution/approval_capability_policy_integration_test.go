//go:build integration

package toolexecution_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/pkg/api"
)

// Exercise installed task authority through policy composition, without NeverAsk.
// Grant minting and handler permit consumption have separate real-Invoke coverage.
func TestTaskCapabilityPreservesLivePolicyAndScope(t *testing.T) {
	for _, satisfied := range []bool{false, true} {
		t.Run(fmt.Sprintf("boundary-satisfied-%t", satisfied), func(t *testing.T) {
			stageApprovals(t, settings.ApprovalEffectAsk)
			root := t.TempDir()
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
			testutil.FailErr(t, "create policy store", err)
			approvals := settings.NewRuleApprovalGate(store, settings.NoSources())
			_, err = approvals.ApplyGrant(hitl.ApprovalGrant{
				ID: "task-host-permission", Scope: hitl.ApprovalGrantScopeChat,
				ProjectID: "project", ChatSessionID: "task",
				Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryExecutionCapability, Pattern: "host_execution"},
			})
			testutil.FailErr(t, "install task capability", err)
			boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{ID: "implement", Tools: map[string]bool{"command": true}}})
			engine := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), approvals)
			action := platform.PolicyContext{
				ProfileID: "implement", ToolName: "command", ProjectDir: root, ProjectID: "project", SessionID: "task",
				ToolArgs: map[string]any{"command": "printf fixture"}, ConfineRequest: confine.Request{HostExecution: true},
				BoundaryApprovalSatisfied: satisfied,
			}
			for _, phase := range []struct {
				name         string
				rule         []settings.ApprovalRule
				session      string
				blocked, ask bool
			}{
				{name: "covered", session: "task"},
				{name: "unrelated-deny", session: "task", rule: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "edit", Effect: settings.ApprovalEffectDeny}}},
				{name: "matching-deny", session: "task", rule: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectDeny}}, blocked: true},
				{name: "recovery", session: "task"},
				{name: "independent-ask", session: "task", rule: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "command", Effect: settings.ApprovalEffectAsk}}, ask: true},
				{name: "foreign-task", session: "other-task", ask: true},
				{name: "original-task-still-covered", session: "task"},
			} {
				t.Run(phase.name, func(t *testing.T) {
					testutil.FailErr(t, "change live project policy", store.PutProject(root, settings.ApprovalConfig{Rules: phase.rule}))
					action.SessionID = phase.session
					decision, err := engine.Evaluate(t.Context(), action)
					testutil.FailErr(t, "evaluate granted capability with live policy", err)
					if decision == nil || decision.Blocked != phase.blocked || decision.RequiresApproval != phase.ask || decision.Allowed != (!phase.blocked && !phase.ask) {
						t.Fatalf("wrong policy disposition: %+v", decision)
					}
					if phase.blocked && decision.RejectCode != "APPROVAL_RULE_DENIED" {
						t.Fatalf("deny enforcement identity lost: %+v", decision)
					}
					if phase.name == "independent-ask" && (decision.Approval == nil || decision.Approval.Decision == nil || decision.Approval.Decision.Primary != api.GateUserRule) {
						t.Fatalf("independent user rule lost: %+v", decision)
					}
				})
			}
		})
	}
}
