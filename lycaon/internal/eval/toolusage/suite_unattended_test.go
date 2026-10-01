package toolusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestUnattendedDeniesAndResumesWithoutGrantingAuthority(t *testing.T) {
	var requests []wire.ResolveCheckpointRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/owned/checkpoints/request" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		var request wire.ResolveCheckpointRequest
		testutil.FailErr(t, "decode rejection", json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, token: "fixture", http: server.Client()}
	result := CaseReport{SessionID: "owned"}
	observe := client.unattendedObserver(t.Context(), &result, UnattendedPolicy{ApprovalGuidance: "Use the project", Answer: "Complete the task", MaxInterventions: 1}, nil, func(CaseReport) error { return nil })
	pending := []wire.CheckpointEvent{{ID: "request", Kind: wire.CheckpointKindToolApproval, Status: wire.CheckpointStatusPending}}
	for range 2 {
		testutil.FailErr(t, "deny request without duplicate response", observe(pending, nil))
	}
	testutil.FailErr(t, "resume after denial", observe(nil, nil))
	if len(requests) != 1 || requests[0].Action != wire.ApprovalActionReject || requests[0].Guidance != "Use the project" || requests[0].OptionID != "" || len(result.AutomaticResponses) != 1 || result.Status != "running" {
		t.Fatalf("incorrect scripted resolution: %+v, %+v", requests, result)
	}
	pending[0].ID = "again"
	var exhausted interactionExhausted
	if !errors.As(observe(pending, nil), &exhausted) || len(requests) != 1 {
		t.Fatal("interaction budget did not end repeated requests")
	}
}

func TestUnattendedAnswersExactRevisionAndPreservesResponseFailure(t *testing.T) {
	responseStatus, count := http.StatusOK, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request wire.ResolveUserFeedbackRequest
		testutil.FailErr(t, "decode fixed answer", json.NewDecoder(r.Body).Decode(&request))
		if r.URL.Path != "/v1/workflow-runs/run/feedback/question" || request.ExpectedRevision != 7 || request.Response != "Use the requirements" {
			t.Errorf("incorrect answer: %s %+v", r.URL, request)
		}
		count++
		w.WriteHeader(responseStatus)
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	result := CaseReport{SessionID: "owned"}
	policy := UnattendedPolicy{ApprovalGuidance: "Stay local", Answer: "Use the requirements", MaxInterventions: 3}
	feedback := &FeedbackRequest{RunID: "run", PendingFeedback: wire.PendingFeedback{PhaseID: "question", IssuedRevision: 7}}
	observe := client.unattendedObserver(t.Context(), &result, policy, nil, func(CaseReport) error { return nil })
	testutil.FailErr(t, "answer question", observe(nil, feedback))
	testutil.FailErr(t, "do not repeat answer", observe(nil, feedback))
	if count != 1 || len(result.AutomaticResponses) != 1 {
		t.Fatal("answer was duplicated or not retained")
	}
	responseStatus = http.StatusConflict
	observe = client.unattendedObserver(t.Context(), &result, policy, nil, func(CaseReport) error { return nil })
	err := observe(nil, feedback)
	var exhausted interactionExhausted
	if err == nil || errors.As(err, &exhausted) {
		t.Fatalf("host response failure became a model failure: %v", err)
	}
}

func TestUnattendedCannotAnswerSecretsOrUnspecifiedChoices(t *testing.T) {
	client := &liveClient{}
	for _, feedback := range []*FeedbackRequest{
		{PendingFeedback: wire.PendingFeedback{Secret: &wire.SecretInputMeta{}}},
		{PendingFeedback: wire.PendingFeedback{ResponseType: "single_choice"}},
	} {
		observe := client.unattendedObserver(t.Context(), &CaseReport{}, UnattendedPolicy{MaxInterventions: 3}, nil, func(CaseReport) error { return nil })
		var exhausted interactionExhausted
		if !errors.As(observe(nil, feedback), &exhausted) {
			t.Fatal("unscripted input was not bounded")
		}
	}
}

func TestFixtureApprovalsDoNotConsumeInterventionBudget(t *testing.T) {
	approvals, refusals := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request wire.ResolveCheckpointRequest
		testutil.FailErr(t, "decode fixture response", json.NewDecoder(r.Body).Decode(&request))
		if request.Action == wire.ApprovalActionApprove {
			approvals++
			if request.OptionID != "owned-once" {
				t.Errorf("unexpected authority: %+v", request)
			}
		} else {
			refusals++
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	result := CaseReport{SessionID: "owned", Sandbox: &SandboxEvidence{Kind: "approve_read", PreparationCallID: "prepared"}}
	observe := client.unattendedObserver(t.Context(), &result, UnattendedPolicy{MaxInterventions: 1}, nil, func(CaseReport) error { return nil })
	checkpoint := wire.CheckpointEvent{Kind: wire.CheckpointKindToolApproval, Status: wire.CheckpointStatusPending,
		ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "prepared", Plan: wire.ApprovalPlan{
			Subject: wire.ApprovalSubject{Kind: wire.ApprovalSubjectKindLoopbackConnect,
				Targets: []wire.ApprovalTarget{{Kind: "loopback_connect", Details: map[string]any{"connect_ports": []any{float64(12345)}}}}},
			Options: []wire.ApprovalOption{{ID: "owned-once", Kind: wire.ApprovalOptionKindCurrentAction,
				Rung: wire.ApprovalOptionRungOnce, DecisionAction: wire.ApprovalOptionDecisionApprove}},
		}}}
	for i := range 6 {
		checkpoint.ID = fmt.Sprintf("approved-%d", i)
		testutil.FailErr(t, "approve exact fixture repeatedly", observe([]wire.CheckpointEvent{checkpoint}, nil))
		testutil.FailErr(t, "do not repeat approved checkpoint", observe([]wire.CheckpointEvent{checkpoint}, nil))
	}
	denied := checkpoint
	denied.ID, denied.ToolApproval = "outside-fixture", nil
	testutil.FailErr(t, "retain intervention after fixture approvals", observe([]wire.CheckpointEvent{denied}, nil))
	checkpoint.ID = "approved-after-refusal"
	testutil.FailErr(t, "approve fixture after intervention exhausted", observe([]wire.CheckpointEvent{checkpoint}, nil))
	denied.ID = "outside-again"
	var exhausted interactionExhausted
	if !errors.As(observe([]wire.CheckpointEvent{denied}, nil), &exhausted) || approvals != 7 || refusals != 1 {
		t.Fatalf("incorrect separate budgets: approvals=%d refusals=%d", approvals, refusals)
	}
}

func TestPreparedDenialDoesNotSpendCandidateInterventions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	result := CaseReport{SessionID: "parent", Sandbox: &SandboxEvidence{Kind: "deny_loopback", PreparationCallID: "prepared"}}
	observe := client.unattendedObserver(t.Context(), &result, UnattendedPolicy{MaxInterventions: 1}, nil, func(CaseReport) error { return nil })
	prepared := wire.CheckpointEvent{ID: "setup", SessionID: "parent", Kind: wire.CheckpointKindToolApproval, Status: wire.CheckpointStatusPending, ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "prepared"}}
	testutil.FailErr(t, "resolve prepared denial", observe([]wire.CheckpointEvent{prepared}, nil))
	candidate := wire.CheckpointEvent{ID: "candidate", SessionID: "child", Kind: wire.CheckpointKindToolApproval, Status: wire.CheckpointStatusPending, ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "prepared"}}
	testutil.FailErr(t, "resolve candidate denial", observe([]wire.CheckpointEvent{candidate}, nil))
	if len(result.AutomaticResponses) != 2 || result.AutomaticResponses[1].SessionID != "child" {
		t.Fatalf("lost intervention origin: %+v", result.AutomaticResponses)
	}
	candidate.ID = "again"
	var exhausted interactionExhausted
	if !errors.As(observe([]wire.CheckpointEvent{candidate}, nil), &exhausted) {
		t.Fatal("candidate allowance was not enforced")
	}
}

func TestApprovalRulesDecideCandidateCardsBySubjectKind(t *testing.T) {
	var requests []wire.ResolveCheckpointRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request wire.ResolveCheckpointRequest
		testutil.FailErr(t, "decode response", json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	result := CaseReport{SessionID: "owned"}
	scripts := []ApprovalRule{{Subject: "write_root_set", Decision: "approve", Rung: "task"}, {Subject: "direct_ip", Decision: "reject"}}
	observe := client.unattendedObserver(t.Context(), &result, UnattendedPolicy{ApprovalGuidance: "Stay local", MaxInterventions: 1}, scripts, func(CaseReport) error { return nil })
	card := func(id string, kind wire.ApprovalSubjectKind) wire.CheckpointEvent {
		return wire.CheckpointEvent{ID: id, SessionID: "owned", Kind: wire.CheckpointKindToolApproval, Status: wire.CheckpointStatusPending,
			ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "candidate-" + id, Plan: wire.ApprovalPlan{Subject: wire.ApprovalSubject{Kind: kind},
				Options: []wire.ApprovalOption{
					{ID: "once", Kind: wire.ApprovalOptionKindCurrentAction, Rung: wire.ApprovalOptionRungOnce, DecisionAction: wire.ApprovalOptionDecisionApprove},
					{ID: "lease", Kind: wire.ApprovalOptionKindLease, Scope: wire.ApprovalGrantScopeChat, Rung: wire.ApprovalOptionRungChat, DecisionAction: wire.ApprovalOptionDecisionApprove},
				}}}}
	}
	testutil.FailErr(t, "approve by rule", observe([]wire.CheckpointEvent{card("root", wire.ApprovalSubjectKindWriteRootSet)}, nil))
	testutil.FailErr(t, "deny by rule", observe([]wire.CheckpointEvent{card("ip", wire.ApprovalSubjectKindDirectIP)}, nil))
	testutil.FailErr(t, "deny again spends the allowance", observe([]wire.CheckpointEvent{card("ip-again", wire.ApprovalSubjectKindDirectIP)}, nil))
	var exhausted interactionExhausted
	if !errors.As(observe([]wire.CheckpointEvent{card("ip-third", wire.ApprovalSubjectKindDirectIP)}, nil), &exhausted) {
		t.Fatal("repeated rule denials must be bounded by the allowance")
	}
	if len(requests) != 3 || requests[0].Action != wire.ApprovalActionApprove || requests[0].OptionID != "lease" || requests[1].Action != wire.ApprovalActionReject || requests[2].Action != wire.ApprovalActionReject {
		t.Fatalf("rule decisions: %+v", requests)
	}
	kinds := []string{}
	for _, response := range result.AutomaticResponses {
		kinds = append(kinds, response.Kind)
	}
	if fmt.Sprint(kinds) != "[fixture_rule_approved fixture_rule_denied fixture_rule_denied]" {
		t.Fatalf("recorded kinds: %v", kinds)
	}
	unruled := card("socket", wire.ApprovalSubjectKindSocketSet)
	if !errors.As(observe([]wire.CheckpointEvent{unruled}, nil), &exhausted) {
		t.Fatal("a card without a rule is an ordinary intervention")
	}
}

func TestUnexpectedChoiceUsesBoundedFallback(t *testing.T) {
	var decisions []wire.ResolveWorkflowDecisionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request wire.ResolveWorkflowDecisionRequest
		testutil.FailErr(t, "decode fallback choice", json.NewDecoder(r.Body).Decode(&request))
		if r.URL.Path != "/v1/workflow-runs/run/decisions/choice" {
			t.Errorf("choice resolved at %s, want the run's decisions route", r.URL.Path)
		}
		decisions = append(decisions, request)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := &liveClient{base: server.URL, http: server.Client()}
	result := CaseReport{SessionID: "owned"}
	policy := UnattendedPolicy{Answer: "Complete the task as written", MaxInterventions: 1}
	observe := client.unattendedObserver(t.Context(), &result, policy, nil, func(CaseReport) error { return nil })
	question := &FeedbackRequest{RunID: "run", PendingFeedback: wire.PendingFeedback{
		PhaseID: "choice", IssuedRevision: 3, ResponseType: "single_choice", Options: []string{"Red", "Blue"},
	}}
	testutil.FailErr(t, "answer unexpected choice", observe(nil, question))
	testutil.FailErr(t, "ignore repeated observation", observe(nil, question))
	question.IssuedRevision++
	var exhausted interactionExhausted
	if !errors.As(observe(nil, question), &exhausted) {
		t.Fatal("another question must exhaust the intervention allowance")
	}
	if len(decisions) != 1 || decisions[0].Choice != "Red" || decisions[0].Comment != policy.Answer || decisions[0].ExpectedRevision != 3 {
		t.Fatalf("fallback choice = %+v", decisions)
	}
	if len(result.AutomaticResponses) != 1 || result.AutomaticResponses[0].Kind != "unregistered_ask_answered" {
		t.Fatalf("fallback responses = %+v", result.AutomaticResponses)
	}
}
