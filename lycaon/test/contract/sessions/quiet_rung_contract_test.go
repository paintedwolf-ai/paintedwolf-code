package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEveryDiscretionaryGateProducesAQuietSubject(t *testing.T) {
	t.Parallel()
	for _, g := range api.AllApprovalGateValues() {
		decision := &gate.Decision{Primary: g, ReasonKey: string(g) + ":subject"}
		subjects := hitl.QuietSubjectsFromDecision(decision, nil, "digest")
		if g == api.GateAgentPolicyChange {
			if len(subjects) != 0 {
				t.Errorf("%s: agent-policy review exposes quiet subjects: %+v", g, subjects)
			}
			continue
		}
		if len(subjects) == 0 {
			t.Errorf("%s: no quiet subject", g)
			continue
		}
		if strings.TrimSpace(subjects[0].Label) == "" {
			t.Errorf("%s: quiet subject has no label", g)
		}
	}
}

func TestInstructionReviewCannotBeQuietedByAnotherGate(t *testing.T) {
	t.Parallel()
	decision := &gate.Decision{Primary: api.GateUserRule, Also: []api.ApprovalGate{api.GateAgentPolicyChange}}
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "write",
		},
		Scope: hitl.ActionScope{
			SessionID:     "chat",
			RootSessionID: "chat",
		},
	}
	if subjects := hitl.QuietSubjectsFromDecision(decision, nil, "digest"); len(subjects) != 0 {
		t.Fatalf("a decision carrying agent policy exposed quiet subjects: %+v", subjects)
	}
	if options := hitl.QuietOptions(action, decision, nil, nil); len(options) != 0 {
		t.Fatalf("a decision carrying agent policy offered quiet: %+v", options)
	}
}

func TestQuietIsNeverTheGlobalSwitch(t *testing.T) {
	t.Parallel()
	mustNarrow := map[api.ApprovalGate]bool{
		api.GateConsentDrift:            true,
		api.GateIncompleteFacts:         true,
		api.GateExplicitApprovalRequest: true,
	}
	for _, g := range api.AllApprovalGateValues() {
		got := gate.ExactActionQuiet(g)
		if want := mustNarrow[g]; got != want {
			t.Errorf("%s: ExactActionQuiet = %v want %v", g, got, want)
		}
	}
	for g := range mustNarrow {
		decision := &gate.Decision{Primary: g, ReasonKey: string(g) + ":constant"}
		a := hitl.QuietSubjectsFromDecision(decision, nil, "digest-a")
		b := hitl.QuietSubjectsFromDecision(decision, nil, "digest-b")
		if len(a) != 1 || len(b) != 1 {
			t.Fatalf("%s: expected one subject per action", g)
		}
		if a[0].Key == b[0].Key {
			t.Errorf("%s: two different actions share a quiet key — the quiet is class-wide", g)
		}
	}
}
