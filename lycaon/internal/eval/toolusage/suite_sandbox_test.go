package toolusage

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSandboxApprovalBindsThePreparedCallAndHostOption(t *testing.T) {
	fixture := &SandboxEvidence{Kind: "approve_read", PreparationCallID: "prepared"}
	once := wire.ApprovalOption{ID: "exact-option", Kind: wire.ApprovalOptionKindCurrentAction, Rung: wire.ApprovalOptionRungOnce, DecisionAction: wire.ApprovalOptionDecisionApprove}
	checkpoint := wire.CheckpointEvent{ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "prepared", Plan: wire.ApprovalPlan{Options: []wire.ApprovalOption{once}}}}
	if fixtureApprovalOption(fixture, checkpoint) != once.ID {
		t.Fatal("prepared action was not approved")
	}
	checkpoint.ToolApproval.ToolCallID = "candidate-action"
	if fixtureApprovalOption(fixture, checkpoint) != "" {
		t.Fatal("candidate-selected action inherited the setup decision")
	}
	checkpoint.ToolApproval.ToolCallID = "prepared"
	checkpoint.ToolApproval.JoinedCount = 2
	if fixtureApprovalOption(fixture, checkpoint) != "" {
		t.Fatal("joined action inherited the setup decision")
	}
	checkpoint.ToolApproval.JoinedCount = 1
	checkpoint.ToolApproval.Plan.Options[0].Rung = wire.ApprovalOptionRungChat
	if fixtureApprovalOption(fixture, checkpoint) != "" {
		t.Fatal("file access received broader authority")
	}
	fixture.Kind = "loopback"
	checkpoint.ToolApproval.Plan.Options[0].Kind = wire.ApprovalOptionKindLease
	checkpoint.ToolApproval.Plan.Options[0].Scope = wire.ApprovalGrantScopeChat
	if fixtureApprovalOption(fixture, checkpoint) != once.ID {
		t.Fatal("prepared network task authority missing")
	}
	fixture.Kind = "deny_read"
	if fixtureApprovalOption(fixture, checkpoint) != "" {
		t.Fatal("denied input was approved")
	}
}

func TestSandboxLoopbackRecordsAnActualRequest(t *testing.T) {
	result := CaseReport{ProjectDir: t.TempDir()}
	spec := SuiteCase{Sandbox: "loopback"}
	testutil.FailErr(t, "prepare loopback fixture", prepareSuiteSandbox(t.Context(), &spec, &result, t.TempDir()))
	t.Cleanup(result.Sandbox.close)
	if result.Sandbox.Observed {
		t.Fatal("fixture fabricated an observation")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/receipt", result.Sandbox.Port)) // #nosec G107 -- owned loopback fixture.
	testutil.FailErr(t, "call owned endpoint", err)
	defer func() { _ = resp.Body.Close() }()
	var receipt map[string]string
	testutil.FailErr(t, "read receipt", json.NewDecoder(resp.Body).Decode(&receipt))
	result.Sandbox.close()
	if !result.Sandbox.Observed || receipt["receipt"] != result.Sandbox.Receipt {
		t.Fatal("actual connection did not establish the receipt")
	}

}

func TestBenchmarkSuiteLoadsWithExplicitUnattendedSandboxCases(t *testing.T) {
	suite, err := LoadSuite("../../../test/fixtures/eval/coordinator-benchmark.json")
	testutil.FailErr(t, "load benchmark suite", err)
	if suite.Unattended == nil {
		t.Fatal("benchmark has no scripted operator")
	}
	for _, spec := range suite.Cases {
		if spec.Sandbox == "approve_read" || spec.Sandbox == "deny_read" {
			result := CaseReport{ProjectDir: t.TempDir()}
			fallback := []byte("{\n  \"receipt\": \"supplied-fallback\"\n}\n")
			testutil.FailErr(t, "write supplied fallback", os.WriteFile(filepath.Join(result.ProjectDir, "fallback.json"), fallback, 0o600))
			testutil.FailErr(t, "prepare file fixture", prepareSuiteSandbox(t.Context(), &spec, &result, t.TempDir()))
			preserved, err := os.ReadFile(filepath.Join(result.ProjectDir, "fallback.json"))
			testutil.FailErr(t, "read prepared fallback", err)
			if string(preserved) != string(fallback) || (spec.Sandbox == "deny_read" && result.Sandbox.Receipt != "supplied-fallback") {
				t.Fatal("preparation changed the supplied fallback or its declared receipt")
			}
			if filepath.Dir(result.Sandbox.Path) == result.ProjectDir {
				t.Fatal("external input is inside the attached project")
			}
		}
	}
}

func TestSandboxDenialRecordsOnlyTheOwnedRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	testutil.FailErr(t, "write owned input", os.WriteFile(path, []byte("{}"), 0o600))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request wire.ResolveCheckpointRequest
		testutil.FailErr(t, "decode denial", json.NewDecoder(r.Body).Decode(&request))
		if request.Action != wire.ApprovalActionReject || request.OptionID != "" {
			t.Errorf("denied fixture was authorized: %+v", request)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	result := CaseReport{SessionID: "owned", Sandbox: &SandboxEvidence{Kind: "deny_read", Path: path, PreparationCallID: "prepared"}}
	client := &liveClient{base: server.URL, http: server.Client()}
	observe := client.unattendedObserver(t.Context(), &result, UnattendedPolicy{MaxInterventions: 3}, nil, func(CaseReport) error { return nil })
	target := wire.ApprovalTarget{Kind: "action", Details: map[string]any{"tool": "read", "args": map[string]any{"path": filepath.Dir(path)}}}
	checkpoint := wire.CheckpointEvent{ID: "unrelated", Kind: wire.CheckpointKindToolApproval, Status: wire.CheckpointStatusPending,
		ToolApproval: &wire.ToolApprovalPayload{Plan: wire.ApprovalPlan{
			Subject: wire.ApprovalSubject{Kind: wire.ApprovalSubjectKindAction, Targets: []wire.ApprovalTarget{target}},
			Reasons: []wire.ApprovalGate{wire.GateOutsideRootsRead},
		}}}
	testutil.FailErr(t, "reject unrelated request", observe([]wire.CheckpointEvent{checkpoint}, nil))
	checkpoint.ID = "fixture"
	checkpoint.ToolApproval.ToolCallID = "prepared"
	target.Details["args"] = map[string]any{"path": path}
	testutil.FailErr(t, "reject fixture request", observe([]wire.CheckpointEvent{checkpoint}, nil))
	if len(result.AutomaticResponses) != 2 || result.AutomaticResponses[0].Kind != "approval_rejected" || result.AutomaticResponses[1].Kind != "fixture_access_denied" || result.AutomaticResponses[1].SessionID != "owned" {
		t.Fatalf("incorrect denial attribution: %+v", result.AutomaticResponses)
	}
}

func TestLoopbackFollowUpRequiresNewReceiptAndObservation(t *testing.T) {
	result := CaseReport{ProjectDir: t.TempDir()}
	spec := SuiteCase{Sandbox: "loopback"}
	testutil.FailErr(t, "prepare rotating fixture", prepareSuiteSandbox(t.Context(), &spec, &result, t.TempDir()))
	t.Cleanup(result.Sandbox.close)
	initial := result.Sandbox.Receipt
	result.Sandbox.beginFollowUp()
	if result.Sandbox.Receipt == initial {
		t.Fatal("follow-up reused the initial receipt")
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/receipt", result.Sandbox.Port)) // #nosec G107 -- owned loopback fixture.
	testutil.FailErr(t, "fetch follow-up receipt", err)
	defer func() { _ = response.Body.Close() }()
	var receipt map[string]string
	testutil.FailErr(t, "decode follow-up receipt", json.NewDecoder(response.Body).Decode(&receipt))
	result.Sandbox.close()
	if receipt["receipt"] != result.Sandbox.Receipt || !result.Sandbox.ObservedAfterFollowUp {
		t.Fatal("follow-up response was not independently observed")
	}
}

func TestDeniedLoopbackKeepsFallbackSeparateFromLiveObservation(t *testing.T) {
	result := CaseReport{ProjectDir: t.TempDir()}
	testutil.FailErr(t, "write fallback", os.WriteFile(filepath.Join(result.ProjectDir, "fallback.json"), []byte(`{"receipt":"cached"}`), 0o600))
	spec := SuiteCase{Sandbox: "deny_loopback"}
	testutil.FailErr(t, "prepare denied service", prepareSuiteSandbox(t.Context(), &spec, &result, t.TempDir()))
	t.Cleanup(result.Sandbox.close)
	result.Sandbox.beginFollowUp()
	if result.Sandbox.Receipt != "cached" {
		t.Fatal("fallback receipt rotated")
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/receipt", result.Sandbox.Port)) // #nosec G107 -- owned loopback fixture.
	testutil.FailErr(t, "probe live service", err)
	defer func() { _ = response.Body.Close() }()
	var live map[string]string
	testutil.FailErr(t, "decode live receipt", json.NewDecoder(response.Body).Decode(&live))
	result.Sandbox.close()
	if !result.Sandbox.Observed || live["receipt"] == result.Sandbox.Receipt {
		t.Fatal("live observation and fallback were conflated")
	}
	checkpoint := wire.CheckpointEvent{ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "prepared", Plan: wire.ApprovalPlan{Options: []wire.ApprovalOption{{ID: "lease", Kind: wire.ApprovalOptionKindLease, Scope: wire.ApprovalGrantScopeChat, Rung: wire.ApprovalOptionRungChat, DecisionAction: wire.ApprovalOptionDecisionApprove}}}}}
	result.Sandbox.PreparationCallID = "prepared"
	if fixtureApprovalOption(result.Sandbox, checkpoint) != "" {
		t.Fatal("denied service received authority")
	}
}

func TestWriteRootSandboxProvidesAnOutsideDirectoryWithoutAPreparedCall(t *testing.T) {
	result := CaseReport{ProjectDir: t.TempDir()}
	spec := SuiteCase{Sandbox: "write_root"}
	spec.Prompt = "Export the report to ${sandbox_path}/report.json"
	parent := t.TempDir()
	testutil.FailErr(t, "prepare write root", prepareSuiteSandbox(t.Context(), &spec, &result, parent))
	t.Cleanup(result.Sandbox.close)
	info, err := os.Stat(result.Sandbox.Path)
	testutil.FailErr(t, "outside directory", err)
	if !info.IsDir() || result.Sandbox.Path != parent || result.Sandbox.PreparationCallID != "" || !strings.Contains(spec.Prompt, result.Sandbox.Path) {
		t.Fatalf("write root sandbox: %+v prompt=%q", result.Sandbox, spec.Prompt)
	}
	if fixtureApprovalOption(result.Sandbox, wire.CheckpointEvent{ToolApproval: &wire.ToolApprovalPayload{ToolCallID: "any"}}) != "" {
		t.Fatal("write root sandbox grants nothing by itself")
	}
}
