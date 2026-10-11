package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
)

func TestCapabilityDenialRequiresExactInvocationIdentity(t *testing.T) {
	executor := NewExecutor(nil, nil, "")
	denied := &hitl.CheckpointResponse{
		Status: hitl.DecisionStatusRejected,
		Result: &hitl.DecisionResult{Comments: "Use the local fixture instead."},
	}

	executor.Rejections.rememberCapabilityDenial("session", "", denied)
	executor.Rejections.rememberCapabilityDenial("", "call", denied)
	if executor.Rejections.takeCapabilityDenial("session", "call") {
		t.Fatal("denial recorded without an exact identity")
	}

	executor.Rejections.rememberCapabilityDenial("session", "call", denied)
	if executor.Rejections.takeCapabilityDenial("session", "other") {
		t.Fatal("denial crossed tool-call identity")
	}
	if !executor.Rejections.takeCapabilityDenial("session", "call") {
		t.Fatal("denial not reported for its own invocation")
	}
	if executor.Rejections.takeCapabilityDenial("session", "call") {
		t.Fatal("denial was not consumed")
	}
}

// Expiry and cancellation are not a person's No: reporting them as denials would
// teach the agent that walking away settled something.
func TestCapabilityDenialRecordsOnlyRejection(t *testing.T) {
	executor := NewExecutor(nil, nil, "")
	for _, response := range []*hitl.CheckpointResponse{
		{Status: hitl.DecisionStatusApproved, Result: &hitl.DecisionResult{Comments: "approved note"}},
		{Status: hitl.DecisionStatusExpired, Result: &hitl.DecisionResult{Comments: "system expiry"}},
		{Status: hitl.DecisionStatusCanceled},
	} {
		executor.Rejections.rememberCapabilityDenial("session", "call", response)
	}
	if executor.Rejections.takeCapabilityDenial("session", "call") {
		t.Fatal("a non-rejection was recorded as a denial")
	}
}

// A refusal can land on a call that still succeeds — one capability denied inside
// a run that kept going — so the record cannot depend on an error value.
func TestCapabilityDenialRecordedWithoutGuidanceOrError(t *testing.T) {
	executor := NewExecutor(nil, nil, "")
	executor.Rejections.rememberCapabilityDenial("session", "call", &hitl.CheckpointResponse{
		Status: hitl.DecisionStatusRejected,
		Result: &hitl.DecisionResult{Comments: "   "},
	})
	if !executor.Rejections.takeCapabilityDenial("session", "call") {
		t.Fatal("denial without attached direction was not recorded")
	}
}

func TestAttachUserGuidanceSetsAgentPublicKey(t *testing.T) {
	reject := &toolrejection.ToolReject{Code: "OUTBOUND_SECRET_DENIED"}
	toolrejection.AttachUserGuidance(reject, "  Use the public endpoint.  ")
	if got := reject.Data[toolrejection.UserGuidanceKey]; got != "Use the public endpoint." {
		t.Fatalf("reject guidance = %#v", got)
	}
	toolrejection.AttachUserGuidance(reject, "   ")
	if got := reject.Data[toolrejection.UserGuidanceKey]; got != "Use the public endpoint." {
		t.Fatalf("blank direction overwrote the attached one: %#v", got)
	}
}

func TestIsolationCheckpointRejectDistinguishesHumanDecisionFromUnavailability(t *testing.T) {
	denied := isolationCheckpointReject(isolation.CodeDirectIPDenied, &hitl.CheckpointResponse{
		Status: hitl.DecisionStatusRejected,
		Result: &hitl.DecisionResult{Comments: "Use mediated egress."},
	})
	if denied.Code != isolation.CodeDirectIPDenied || denied.Data[toolrejection.UserGuidanceKey] != "Use mediated egress." {
		t.Fatalf("human decision reject = %+v", denied)
	}
	for name, response := range map[string]*hitl.CheckpointResponse{
		"missing":  nil,
		"expired":  {Status: hitl.DecisionStatusExpired},
		"canceled": {Status: hitl.DecisionStatusCanceled},
		"pending":  {Status: hitl.DecisionStatusPending},
	} {
		t.Run(name, func(t *testing.T) {
			reject := isolationCheckpointReject(isolation.CodeDirectIPDenied, response)
			if reject.Code != isolation.CodeApprovalUnavailable {
				t.Fatalf("unavailable approval reject = %+v", reject)
			}
		})
	}
}
