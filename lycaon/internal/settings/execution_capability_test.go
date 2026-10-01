package settings

import (
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskExecutionPermissionCoversDifferentCommandsAndRevokes(t *testing.T) {
	approvals := ladderGate(t)
	action := hitl.ProposedAction{Tool: "command", Args: map[string]any{"command": "sudo -n id"}, SessionID: "task", ProjectID: "project", Contained: hitl.Contained{HostExecution: true}}
	decision := &gate.Decision{Posture: gate.PostureBalanced, Primary: api.GateUnobservedChannel, ReasonKey: "unobserved_channel:host_execution", Cited: []gate.Fact{{Gate: api.GateUnobservedChannel, Key: "boundary.execution", Value: "host_execution", Source: "host"}}}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: decision})
	if len(offers) != 3 {
		t.Fatalf("expected existing day/task/disabled project ladder: %+v", offers)
	}
	task := offers[1]
	if task.Rung != hitl.ApprovalRungChat || task.Disabled || task.Grant.Predicate.Category != hitl.ApprovalGrantCategoryExecutionCapability || len(task.Grant.ExactActionSet) != 0 {
		t.Fatalf("not task-wide capability: %+v", task)
	}

	options := []hitl.ApprovalOption{hitl.CurrentActionOption()}
	for _, offer := range offers {
		options = append(options, hitl.GrantOption(offer))
	}
	primary, cited, reasons := hitl.PresentDecision(decision)
	plan, err := hitl.NewApprovalPlan(action, hitl.ApprovalStagePreSpawn, hitl.ApprovalSubject{Kind: hitl.ApprovalSubjectHostExecution, Title: hitl.HostExecutionTitle, Targets: []hitl.ApprovalTarget{{Kind: "host_execution", Label: "sudo -n id"}}}, hitl.ApprovalPresentation{Action: hitl.HostExecutionTitle, Impact: hitl.HostExecutionWhat, Gate: primary, Cited: cited}, reasons, options, hitl.FaceContext{})
	testutil.FailErr(t, "build balanced approval plan", err)
	if plan.RecommendedOptionID != task.ID {
		t.Fatalf("balanced primary %s is not task permission %s", plan.RecommendedOptionID, task.ID)
	}
	if !offers[2].Disabled {
		t.Fatal("project permission offered")
	}
	_, err = approvals.ApplyGrant(task.Grant)
	testutil.FailErr(t, "install task permission", err)
	for i := range 15 {
		action.Args = map[string]any{"command": fmt.Sprintf("sudo -n example-operation-%d", i)}
		if !approvals.executionCapabilityCovers(action) {
			t.Fatalf("command %d asks again", i)
		}
		facts, _, _ := approvals.factsForAction(action, ApprovalConfig{}, ApprovalRuleLayers{})
		if facts.Leased || facts.LeasedExact {
			t.Fatal("capability suppressed unrelated policy")
		}
		verdict, nextDecision := gate.Evaluate(facts, gate.PostureBalanced)
		if verdict != gate.Silent {
			t.Fatalf("command %d asks again: %+v", i, nextDecision)
		}

		if !facts.ExecutionCapabilityLeased {
			t.Fatal("task permission absent from gate facts")
		}
	}
	facts, _, _ := approvals.factsForAction(action, ApprovalConfig{}, ApprovalRuleLayers{})
	facts.UserRule = &gate.UserRule{Category: "tool", Pattern: "command", Subject: "command"}
	verdict, independent := gate.Evaluate(facts, gate.PostureBalanced)
	if verdict != gate.Ask || independent.Primary != api.GateUserRule {
		t.Fatalf("task capability suppressed independent rule: %+v", independent)
	}
	foreign := action
	foreign.SessionID = "other"
	if approvals.executionCapabilityCovers(foreign) {
		t.Fatal("permission crossed tasks")
	}
	foreign = action
	foreign.Contained = hitl.Contained{ProcessControl: true}
	if approvals.executionCapabilityCovers(foreign) {
		t.Fatal("permission changed capability")
	}
	approvals.grants.revoke(task.Grant.ID)
	if approvals.executionCapabilityCovers(action) {
		t.Fatal("revoked permission covers action")
	}
	expired := task.Grant
	past := time.Now().Add(-time.Minute)
	expired.ExpiresAt = &past
	approvals.grants.put(expired)
	if approvals.executionCapabilityCovers(action) {
		t.Fatal("expired permission covers action")
	}
}

func TestExecutionCapabilityCannotBecomeDurablePolicy(t *testing.T) {
	approvals := ladderGate(t)
	grant := hitl.ApprovalGrant{ID: "invalid", Scope: hitl.ApprovalGrantScopeDevice, ChatSessionID: "task", Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryExecutionCapability, Pattern: "host_execution"}}
	if _, err := approvals.ApplyGrant(grant); err == nil {
		t.Fatal("accepted durable execution permission")
	}
	if allowedPolicyCategories[ApprovalCategory(hitl.ApprovalGrantCategoryExecutionCapability)] || allowedDurableGrantCategories[ApprovalCategory(hitl.ApprovalGrantCategoryExecutionCapability)] {
		t.Fatal("task permission became authorable durable policy")
	}
}
