//go:build integration

package toolexecution_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
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

// The real gate owns reviewed-reason coverage; the supplied detection is a
// structured producer fact, not a simulated gate verdict or an OS-boundary claim.
type reviewDetectionSource struct{ match hitl.DetectionMatch }

func (s reviewDetectionSource) MatchAction(hitl.ProposedAction, gate.Posture) (hitl.DetectionMatch, bool) {
	return s.match, true
}

func TestCurrentReviewTracksDetectionEffectsRatherThanCorrelation(t *testing.T) {
	stageApprovals(t, settings.ApprovalEffectAsk)
	root := t.TempDir()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create review policy store", err)
	match := hitl.DetectionMatch{PackID: "fixture", RuleID: "same-rule", RuleTitle: "Declared local effect", Level: "high", Local: true, Tagged: true, CorrelationID: "first"}
	sources := settings.NoSources()
	sources.Detections = func() settings.DetectionSource { return reviewDetectionSource{match: match} }
	approvals := settings.NewRuleApprovalGate(store, sources)
	args := map[string]any{"command": "printf fixture"}
	req := confine.Request{Roots: []string{root}}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "task", ToolCallID: "call"}, Invocation: tools.Invocation{ID: "child"}, Effects: tools.InvocationEffects{CanonicalArgs: args}}
	action := hitl.ProposedAction{
		Scope:      hitl.ActionScope{SessionID: "task", ProjectDir: root},
		Invocation: hitl.ActionInvocation{Tool: "command", Args: args, ActionID: tc.EffectActionID()},
		Execution:  hitl.ActionExecution{Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy}},
	}
	pending, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate first detection", err)
	if pending == nil || !pending.Required() || pending.Gate() != api.GateAuthorityMisuse {
		t.Fatalf("fixture did not require detection review: %+v", pending)
	}
	tc.RecordCurrentActionReview(args, req, action, pending)
	for _, phase := range []struct {
		name               string
		changedEffect, ask bool
	}{
		{name: "unchanged"}, {name: "correlation-only"}, {name: "same-rule-changed-effect", changedEffect: true, ask: true},
	} {
		t.Run(phase.name, func(t *testing.T) {
			match.CorrelationID = phase.name
			match.Unrecoverable = phase.changedEffect
			action.Invocation.CurrentActionReview = tc.CurrentActionReview(args, req)
			result, err := approvals.Evaluate(t.Context(), action)
			testutil.FailErr(t, "reevaluate structured detection", err)
			if result == nil || result.Required() != phase.ask || result.Denied {
				t.Fatalf("wrong reviewed-effect disposition: %+v", result)
			}
			if phase.ask {
				if result.Gate() != api.GateAuthorityMisuse {
					t.Fatalf("changed effect lost its review reason: %+v", result)
				}
				tc.RecordCurrentActionReview(args, req, action, result)
			}
		})
	}
	action.Invocation.CurrentActionReview = tc.CurrentActionReview(args, req)
	result, err := approvals.Evaluate(t.Context(), action)
	testutil.FailErr(t, "evaluate newly reviewed effect", err)
	if result == nil || !result.AutoApproved() {
		t.Fatalf("reviewed new effect asked again: %+v", result)
	}
}
