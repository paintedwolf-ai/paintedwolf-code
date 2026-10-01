package guidance

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestComposeToolResultCompleted(t *testing.T) {
	tr := ComposeToolResult("package main\n", ToolResultFacts{}, nil)
	if tr == nil {
		t.Fatal("expected tool result")
	}
	if tr.Outcome != api.ToolResultOutcomeCompleted {
		t.Fatalf("outcome = %q want completed", tr.Outcome)
	}
	if tr.UiVisibility != api.ToolResultUiVisibilityNormal {
		t.Fatalf("ui_visibility = %q want normal", tr.UiVisibility)
	}
	if len(tr.Codes) != 0 {
		t.Fatalf("codes = %v want none", tr.Codes)
	}
}

// Quiet chrome follows the registry row for the raised code; the body never
// enters the decision.
func TestComposeToolResultUiVisibilityFollowsRegistry(t *testing.T) {
	bundled, err := LoadHintConfig(extpacks.Bundled(hintregistry.DefaultDir))
	testutil.FailErr(t, "LoadHintConfig failed", err)
	stock, err := LoadHintConfigStock()
	testutil.FailErr(t, "LoadHintConfigStock failed", err)

	for name, tc := range map[string]struct {
		hints   *HintConfig
		facts   ToolResultFacts
		outcome api.ToolResultOutcome
		vis     api.ToolResultUiVisibility
	}{
		"coordinator read reject is benign": {
			hints:   bundled,
			facts:   ToolResultFacts{Outcome: api.ToolResultOutcomeRejected, Codes: []string{"COORDINATOR_READ_OUTSIDE_SCOPE"}},
			outcome: api.ToolResultOutcomeRejected,
			vis:     api.ToolResultUiVisibilityBenign,
		},
		"progress missing is benign": {
			hints:   stock,
			facts:   ToolResultFacts{Outcome: api.ToolResultOutcomeRejected, Codes: []string{"PROGRESS_MISSING"}},
			outcome: api.ToolResultOutcomeRejected,
			vis:     api.ToolResultUiVisibilityBenign,
		},
		"code with no registry row is benign": {
			hints:   nil,
			facts:   ToolResultFacts{Outcome: api.ToolResultOutcomeRejected, Codes: []string{"PROGRESS_MISSING"}},
			outcome: api.ToolResultOutcomeRejected,
			vis:     api.ToolResultUiVisibilityBenign,
		},
		"refusal with no code stays visible": {
			hints:   nil,
			facts:   ToolResultFacts{Outcome: api.ToolResultOutcomeRejected},
			outcome: api.ToolResultOutcomeRejected,
			vis:     api.ToolResultUiVisibilityNormal,
		},
		"spec posture reject is normal": {
			hints:   stock,
			facts:   ToolResultFacts{Outcome: api.ToolResultOutcomeRejected, Codes: []string{"SPEC_POSTURE_STUB_REQUIRED"}},
			outcome: api.ToolResultOutcomeRejected,
			vis:     api.ToolResultUiVisibilityNormal,
		},
		"informational banner on a completed call is benign": {
			hints:   bundled,
			facts:   ToolResultFacts{Codes: []string{"BOARD_EMPTY_SKIP_TO_VERIFY"}},
			outcome: api.ToolResultOutcomeCompleted,
			vis:     api.ToolResultUiVisibilityBenign,
		},
		"worker dispatch stays on the transcript": {
			hints:   bundled,
			facts:   ToolResultFacts{Codes: []string{"BANNER_TASK_QUEUED"}},
			outcome: api.ToolResultOutcomeCompleted,
			vis:     api.ToolResultUiVisibilityNormal,
		},
		"phase exit required is visible": {
			hints:   bundled,
			facts:   ToolResultFacts{Codes: []string{"WORKFLOW_PHASE_EXIT_REQUIRED"}},
			outcome: api.ToolResultOutcomeCompleted,
			vis:     api.ToolResultUiVisibilityNormal,
		},
	} {
		t.Run(name, func(t *testing.T) {
			tr := ComposeToolResult("body", tc.facts, tc.hints)
			if tr == nil {
				t.Fatal("expected tool result")
			}
			if tr.Outcome != tc.outcome {
				t.Fatalf("outcome = %q want %q", tr.Outcome, tc.outcome)
			}
			if tr.UiVisibility != tc.vis {
				t.Fatalf("ui_visibility = %q want %q", tr.UiVisibility, tc.vis)
			}
		})
	}
}

// Every raised code reaches the wire in raise order.
func TestComposeToolResultCarriesEveryRaisedCode(t *testing.T) {
	facts := ToolResultFacts{}.
		WithCode("BANNER_TASK_QUEUED").
		WithCode("BANNER_WORKER_INFLIGHT_ROSTER")
	tr := ComposeToolResult("body", facts, nil)
	if len(tr.Codes) != 2 {
		t.Fatalf("codes = %v want both raised codes", tr.Codes)
	}
	if tr.Codes[0] != "BANNER_TASK_QUEUED" || tr.Codes[1] != "BANNER_WORKER_INFLIGHT_ROSTER" {
		t.Fatalf("codes = %v want raise order preserved", tr.Codes)
	}
	if tr.PrimaryCode() != "BANNER_TASK_QUEUED" {
		t.Fatalf("primary = %q want the first raised", tr.PrimaryCode())
	}
}

// TestComposeToolResultIgnoresRefusalProseInTheBody keeps outcome authority in facts.
func TestComposeToolResultIgnoresRefusalProseInTheBody(t *testing.T) {
	for name, body := range map[string]string{
		"file documenting the errno family": `{"path":"docs/security.md","content":"63\tOne EACCES reaches the host as permission denied, PermissionDenied, EACCES, or Access is denied depending on the runtime."}`,
		"file naming the permission system": `{"path":"AGENTS.md","content":"1\tDetection packs may raise additional approval asks on top of the permission system."}`,
		"fetched page with a kiss code":     "[https://www.rfc-editor.org/rfc/rfc5905.txt] lines 1980-1982\n1981|   | 4 access denied          | The access controls have blacklisted",
		"command output quoting a denial":   `{"tail":"grep: permission denied\n","ok":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			tr := ComposeToolResult(body, ToolResultFacts{}, nil)
			if tr == nil {
				t.Fatal("expected tool result")
			}
			if tr.Outcome != api.ToolResultOutcomeCompleted {
				t.Fatalf("outcome = %q want completed — the body is data, not evidence about this call", tr.Outcome)
			}
		})
	}
}

// The producer's statement is what lands, including for a call that failed.
func TestComposeToolResultCarriesStatedFailure(t *testing.T) {
	tr := ComposeToolResult("touch: /root/x: permission denied", ToolResultFacts{
		Outcome: api.ToolResultOutcomeError,
	}, nil)
	if tr.Outcome != api.ToolResultOutcomeError {
		t.Fatalf("outcome = %q want error", tr.Outcome)
	}
}

func TestApplyTaskDispatchMetadata(t *testing.T) {
	tr := &api.ToolResult{Content: "Worker queued"}
	dispatch := &api.WorkerDispatch{WorkerID: "job-abc", ChildSessionID: "child-abc"}
	ApplyTaskDispatchMetadata("task", tr, dispatch)
	if tr.Tool != "task" {
		t.Fatalf("tool = %q want task", tr.Tool)
	}
	if tr.Dispatch == nil || tr.Dispatch.WorkerID != "job-abc" || tr.Dispatch.ChildSessionID != "child-abc" {
		t.Fatalf("dispatch = %+v", tr.Dispatch)
	}
	if tr.UiVisibility != api.ToolResultUiVisibilityNormal {
		t.Fatalf("ui_visibility = %q want normal", tr.UiVisibility)
	}
}

func TestApplyDelegateDispatchMetadata(t *testing.T) {
	tr := &api.ToolResult{Content: "Worker dispatched"}
	ApplyTaskDispatchMetadata("delegate_dispatch", tr, &api.WorkerDispatch{WorkerID: "job-dd", LegID: "leg-1"})
	if tr.Dispatch == nil || tr.Dispatch.WorkerID != "job-dd" {
		t.Fatalf("dispatch = %+v", tr.Dispatch)
	}
	if tr.UiVisibility != api.ToolResultUiVisibilityNormal {
		t.Fatalf("ui_visibility = %q want normal", tr.UiVisibility)
	}
}

func TestApplyTaskDispatchMetadataIgnoresMissingDispatch(t *testing.T) {
	tr := &api.ToolResult{Content: "No dispatch"}
	ApplyTaskDispatchMetadata("task", tr, nil)
	if tr.Dispatch != nil {
		t.Fatalf("dispatch = %+v", tr.Dispatch)
	}
}
