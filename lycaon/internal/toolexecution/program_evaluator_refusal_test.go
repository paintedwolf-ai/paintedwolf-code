package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func evaluatorScreen(t *testing.T, memory *toolcontract.ProgramMemory) ProgramDiagnosticScreen {
	t.Helper()
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build screen", err)
	matcher.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Name: "token", Secret: "fixture-evaluator-protected-value", RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch, NonDisclosable: true}}
	})
	return invocation.ProgramInputCapture{State: memory, Projector: captureprojection.New(matcher, nil), Screening: captureprojection.StreamOptions{WorkingMemory: memory, Bounds: secretmatch.StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 30}}, ProgramID: "program", Scope: captureprojection.Scope{RootSessionID: "session", SessionID: "session", ProjectID: "project"}, MaximumNodes: 1024, MaximumDepth: 32}
}

func TestProgramEvaluatorRefusalRetainsActualCauseAndDelivery(t *testing.T) {
	parent, tc := frameFixture(t)
	tc.Identity.Agent = "implement"
	memory := stageMemory(t, 64<<20)
	args := map[string]any{"command": "cat < original"}
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: args}, memory)
	testutil.FailErr(t, "freeze actual call", err)
	defer frame.Close()
	baseline := memory.InUse()
	args["command"] = "caller replaced arguments"
	cause := &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "evaluation_memory_bytes", "resource_unit": "bytes", "maximum": int64(1024), "observed": int64(1025), "source_line": 2, "source_column": 7, "native_operation": "invented", "secret": "fixture-evaluator-protected-value"}}
	executor := NewExecutor(nil, tools.NewDefaultRegistry(), "implement")
	executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(moduleHintConfig(t)))
	executor.Rejections.SetBlockPlane(stockBlockPlane(t))
	delivery, err := executor.Rejections.RenderProgramRefusal(t.Context(), frame, memory, evaluatorScreen(t, memory), tc, cause, nil)
	testutil.FailErr(t, "render actual evaluator refusal", err)
	defer delivery.Close()
	rendered, ok := guidance.RefusalFromError(delivery.Feedback)
	reject := toolrejection.AsToolReject(delivery.Feedback)
	if !ok || rendered.Code() != cause.Code || len(rendered.Copy) == 0 || reject == nil || reject.ArgumentValidation || reject.Data["native_operation"] != nil || reject.Data["secret"] == cause.Data["secret"] || !errors.Is(delivery.Feedback, cause) {
		t.Fatalf("evaluator provenance lost: delivery=%+v reject=%+v", delivery, reject)
	}
	observed, err := frame.ModelInvocation(t.Context())
	testutil.FailErr(t, "observe original model call", err)
	var captured map[string]any
	testutil.FailErr(t, "decode retained model envelope", json.Unmarshal([]byte(observed.CanonicalArguments), &captured))
	if captured["command"] != "cat < original" || reject.Data["tool"] != "command" || reject.Data["source_line"] != 2 || reject.Data["source_column"] != 7 || memory.InUse().State <= baseline.State {
		t.Fatalf("actual context or retained facts changed: %+v %+v", captured, reject)
	}
	// The delivery lease survives the caller's encoding of the model result.
	_, err = json.Marshal(map[string]any{"code": rendered.Code(), "feedback": rendered.Error(), "details": rendered.Facts.Feedback})
	testutil.FailErr(t, "serialize retained refusal", err)
	if memory.InUse().State <= baseline.State {
		t.Fatal("delivery admission ended before serialization")
	}
	t.Logf("Actual evaluator feedback:\n%s", rendered.Error())
	delivery.Close()
	delivery.Close()
	if memory.InUse() != baseline {
		t.Fatalf("feedback lease leaked: %+v baseline %+v", memory.InUse(), baseline)
	}
	frame.Close()
	if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatal("frame leaked after delivery")
	}
}

func TestProgramEvaluatorRefusalRejectsChangedFrameAndCleansAdmission(t *testing.T) {
	for _, change := range []string{"closed", "call", "surface", "worker", "root", "branch", "pool"} {
		t.Run(change, func(t *testing.T) {
			parent, tc := frameFixture(t)
			memory := stageMemory(t, 64<<20)
			frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat < original"}}, memory)
			testutil.FailErr(t, "freeze frame", err)
			defer frame.Close()
			screen := evaluatorScreen(t, memory)
			ctx := t.Context()
			switch change {
			case "closed":
				frame.Close()
			case "call":
				tc.Identity.ToolCallID = "other"
			case "surface":
				tc.Turn.TurnSurfaceID = "other"
			case "worker":
				tc.Source.WorkerBranchRoot = t.TempDir()
			case "root":
				tc.Source.Roots[0].Path = t.TempDir()
			case "branch":
				tc.Source.ProjectRootBranches = map[string]sourcebranch.ID{"root": "other"}
			case "pool":
				memory = stageMemory(t, 64<<20)

			}
			before := memory.InUse()
			delivery, err := (&Rejections{}).RenderProgramRefusal(ctx, frame, memory, screen, tc, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "program_state_bytes"}}, nil)
			if delivery != nil || err == nil || memory.InUse() != before {
				t.Fatalf("invalid frame retained feedback: delivery=%+v err=%v usage=%+v before=%+v", delivery, err, memory.InUse(), before)
			}
		})
	}
}

type incompleteRefusalScreen struct{ empty bool }

func (s incompleteRefusalScreen) ProjectOperationDiagnostic(ctx context.Context, state toolcontract.ReviewStateAdmission, data map[string]any) (map[string]any, func(), error) {
	release, err := state.ReserveProgramState(ctx, 4096)
	if err != nil {
		return nil, nil, err
	}
	if s.empty {
		return nil, release, nil
	}
	return nil, release, errors.New("screen failed after partial admission")
}
func TestProgramEvaluatorRefusalReleasesIncompleteScreen(t *testing.T) {
	for _, empty := range []bool{false, true} {
		parent, tc := frameFixture(t)
		memory := stageMemory(t, 64<<20)
		frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat < input"}}, memory)
		testutil.FailErr(t, "freeze frame", err)
		baseline := memory.InUse()
		delivery, err := (&Rejections{}).RenderProgramRefusal(t.Context(), frame, memory, incompleteRefusalScreen{empty}, tc, &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "program_state_bytes"}}, nil)
		if delivery == nil || err != nil || delivery.Failure == nil || delivery.Failure.Kind != "unavailable" || delivery.Feedback != nil || delivery.Details != nil || memory.InUse() != baseline {
			t.Fatalf("incomplete screen delivery: %+v %v %+v", delivery, err, memory.InUse())
		}
		delivery.Close()
		frame.Close()
		if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatal("screen failure leaked frame state")
		}
	}
}

func TestProgramEvaluatorRefusalPrepaidFailureKeepsValidFrameAndCause(t *testing.T) {
	for _, mode := range []string{"capacity", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			parent, tc := frameFixture(t)
			memory := stageMemory(t, 64<<20)
			frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat < input"}}, memory)
			testutil.FailErr(t, "freeze prepaid frame", err)
			defer frame.Close()
			baseline := memory.InUse()
			ctx := t.Context()
			release := func() {}
			if mode == "capacity" {
				release = exhaustState(t, memory, 64<<20)
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := memory.InUse()
			cause := &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"secret": "fixture-evaluator-protected-value"}}
			delivery, err := (&Rejections{}).RenderProgramRefusal(ctx, frame, memory, evaluatorScreen(t, memory), tc, cause, nil)
			testutil.FailErr(t, "retain failed delivery beneath valid frame", err)
			if delivery == nil || delivery.Code != cause.Code || delivery.Failure == nil || delivery.Failure.Kind != mode || delivery.Feedback != nil || delivery.Details != nil || memory.InUse() != before {
				t.Fatalf("prepaid failure %+v usage %+v", delivery, memory.InUse())
			}
			if mode == "capacity" {
				var actual *toolcontract.ProgramMemoryLimitError
				if !errors.As(delivery.failureCause, &actual) || delivery.Failure.Capacity == nil || delivery.Failure.Capacity.Maximum != actual.Maximum || delivery.Failure.Capacity.Observed != actual.Observed || delivery.Failure.Capacity.ResourceUnit != "bytes" {
					t.Fatalf("capacity facts %+v actual %+v", delivery.Failure, actual)
				}
			}
			repeated, err := (&Rejections{}).RenderProgramRefusal(ctx, frame, memory, evaluatorScreen(t, memory), tc, cause, nil)
			if err != nil || repeated != delivery || memory.InUse() != before {
				t.Fatalf("repeated failed delivery multiplied state: %+v %v", repeated, err)
			}
			delivery.Close()
			release()
			if memory.InUse() != baseline {
				t.Fatalf("failed delivery leaked %+v", memory.InUse())
			}
			frame.Close()
			if memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
				t.Fatal("prepaid allowance leaked")
			}
		})
	}
}

func TestProgramEvaluatorRefusalDoesNotMaskUnknownPreparationFailure(t *testing.T) {
	parent, tc := frameFixture(t)
	memory := stageMemory(t, 64<<20)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat input"}}, memory)
	testutil.FailErr(t, "freeze actual frame", err)
	defer frame.Close()
	before := memory.InUse()
	original := errors.Join(&toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED"}, &toolcontract.ProgramMemoryLimitError{Account: "state", Maximum: 123, Observed: 124})
	provenance := errors.New("fixture ledger attribution mismatch")
	delivery, err := (&Rejections{}).RenderProgramRefusal(t.Context(), frame, memory, evaluatorScreen(t, memory), tc, original, provenance)
	if delivery != nil || !errors.Is(err, provenance) || memory.InUse() != before {
		t.Fatalf("provenance failure became capacity delivery: %+v %v", delivery, err)
	}
}

func TestProgramFramePrepaysFeedbackBeforeCapture(t *testing.T) {
	parent, tc := frameFixture(t)
	memory := stageMemory(t, programFeedbackAllowance-1)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "printf changed > output"}}, memory)
	var limit *toolcontract.ProgramMemoryLimitError
	if frame.lifetime != nil || !errors.As(err, &limit) || limit.Account != "state" || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("frame escaped prepaid delivery admission: %+v %v %+v", frame, err, memory.InUse())
	}
}
