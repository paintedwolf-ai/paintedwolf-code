package conformance_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestRun3StrictForeignLoopbackUsesOneChatCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "local service\n")
	}))
	t.Cleanup(server.Close)
	c := fixtureCase{
		ID:      "run3-strict-foreign-loopback",
		Program: "curl -s '" + server.URL + "'",
	}
	runner := newConformanceRunnerWithConfig(t, fixtureCorpus{Cases: []fixtureCase{c}}, gate.PostureStrict, false)
	obs := runner.runCaseWithRecommendedApprovals(t, c, 1)
	if obs.TopLevelReject != "" || obs.Outcome.Refusal != nil || obs.Outcome.State != "completed" || obs.Outcome.Status != 0 || obs.Outcome.Stdout != "local service\n" {
		t.Fatalf("approved command = %+v", obs)
	}
	plan := obs.ApprovalPlans[0]
	if !slices.Contains(plan.Reasons, wire.GateCapabilityWidening) {
		t.Fatalf("loopback card lacks capability gate: %+v", plan)
	}
	face := slices.IndexFunc(plan.Options, func(option wire.ApprovalOption) bool { return option.ID == plan.RecommendedOptionID })
	if face < 0 || plan.Options[face].Rung != "chat" {
		t.Fatalf("loopback recommendation does not cover this chat: %+v", plan)
	}
}

func TestRun3EvalSyntaxRefusalPreservesEarlierOutput(t *testing.T) {
	c := fixtureCase{ID: "run3-eval-invalid-source", Program: "printf 'before\\n'; eval 'if'"}
	runner := newConformanceRunnerWithConfig(t, fixtureCorpus{Cases: []fixtureCase{c}}, gate.PostureLight, true)
	obs := runner.runCase(t, c)
	if obs.TopLevelReject != "" || obs.Outcome.Refusal == nil || obs.Outcome.Refusal.Code != "COMMAND_SYNTAX_UNSUPPORTED" {
		t.Fatalf("eval source refusal = %+v", obs)
	}
	if obs.Outcome.Stdout != "before\n" || obs.Outcome.Status == 0 || obs.Outcome.Stderr == "" {
		t.Fatalf("eval refusal lost its earlier result or shell diagnostic: %+v", obs.Outcome)
	}
}
