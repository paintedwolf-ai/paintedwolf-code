package toolexecution

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestProgramFrameRetainsActualModelArgumentsAndSharedLifetime(t *testing.T) {
	parent, tc := frameFixture(t)
	memory := &stageAdmissionFixture{}
	submitted := map[string]any{"script": "cat 'original'", "progress_update": map[string]any{"content": "- [ ] Actual submitted work"}}
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: submitted}, memory)
	testutil.FailErr(t, "capture actual model invocation", err)
	defer frame.Close()
	submitted["script"] = "caller changed"
	submitted["progress_update"].(map[string]any)["content"] = "caller changed"
	observed, err := frame.ModelInvocation(t.Context())
	testutil.FailErr(t, "observe submitted input", err)
	if observed.ToolName != "command" || observed.ToolCallID != tc.Identity.ToolCallID || observed.CanonicalArguments != `{"progress_update":{"content":"- [ ] Actual submitted work"},"script":"cat 'original'"}` || memory.held == 0 {
		t.Fatalf("actual model observation lost: %+v", observed)
	}
	copied := frame
	copied.Close()
	frame.Close()
	if memory.held != 0 {
		t.Fatal("frame did not release aggregate retention")
	}
	if _, err := frame.ModelInvocation(t.Context()); err == nil {
		t.Fatal("closed frame published submitted input")
	}
	if _, err := frame.ChildContext(t.Context(), nativeDefinition(t, "read"), parent, tc); err == nil {
		t.Fatal("closed frame admitted child")
	}
	if err := copied.validateHostContext(t.Context(), tc); err == nil {
		t.Fatal("copied closed frame remained usable")
	}
}

func TestProgramFrameModelCaptureRefusesBeforeRetention(t *testing.T) {
	parent, tc := frameFixture(t)
	cause := errors.New("host state limit")
	memory := &stageAdmissionFixture{err: cause}
	model := ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"script": "true"}}
	if _, err := FreezeProgramFrame(t.Context(), parent, tc, model, memory); !errors.Is(err, cause) {
		t.Fatalf("admission refusal lost: %v", err)
	}
	memory.err = nil
	model.ToolName = "read"
	if _, err := FreezeProgramFrame(t.Context(), parent, tc, model, memory); err == nil {
		t.Fatal("fabricated outer tool admitted")
	}
	model.ToolName = "command"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := FreezeProgramFrame(ctx, parent, tc, model, memory); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled capture admitted")
	}
	if memory.held != 0 {
		t.Fatal("refused frame leaked state")
	}
	tc.Source.Roots = append(tc.Source.Roots, tc.Source.Roots[0])
	if _, err := FreezeProgramFrame(t.Context(), parent, tc, model, memory); err == nil || memory.held != 0 {
		t.Fatal("invalid addressed roots retained frame")
	}
}

func TestProgramFrameCloseWaitsForAdmittedUseBeforeClearingFacts(t *testing.T) {
	parent, tc := frameFixture(t)
	memory := &stageAdmissionFixture{}
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"script": "true"}}, memory)
	testutil.FailErr(t, "capture frame", err)
	end, err := frame.beginUse()
	testutil.FailErr(t, "hold actual owner lifetime", err)
	closed := make(chan struct{})
	go func() { frame.Close(); close(closed) }()
	<-closed
	if memory.held == 0 || len(frame.lifetime.facts.roots) == 0 {
		t.Fatal("Close released facts during an admitted use")
	}
	if _, err := frame.beginUse(); err == nil {
		t.Fatal("Close admitted a new use")
	}
	end()
	end()
	if memory.held != 0 || frame.lifetime.facts.roots != nil || frame.lifetime.facts.parent.ActorID != "" || frame.lifetime.model.CanonicalArguments != "" {
		t.Fatal("drained frame retained unaccounted facts")
	}
}

func TestProgramPolicyObservationDefersIntrinsicRejectionUntilSettlement(t *testing.T) {
	parent, tc := frameFixture(t)
	memory := &stageAdmissionFixture{}
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"script": "cat input"}}, memory)
	testutil.FailErr(t, "freeze actual command", err)
	defer frame.Close()
	baseline := memory.held
	ctx, observation, release, err := prepareProgramPolicyObservation(t.Context(), frame, memory, tc, nil)
	testutil.FailErr(t, "admit actual policy observation", err)
	if observation.submitted["script"] != "cat input" || memory.held <= baseline {
		t.Fatal("policy observation lost actual envelope or decoded-state admission")
	}
	cause := &toolrejection.ToolReject{Code: "READ_PATH_DENIED", Data: map[string]any{"path": "input"}}
	executor := NewExecutor(nil, nil, "fixture")
	if got := executor.Rejections.rejectBeforeInvoke(ctx, "read", "fixture", map[string]any{"path": "input"}, cause); got != cause {
		t.Fatal("native preinvoke rejection was rendered before receipt settlement")
	}
	if got := executor.Rejections.settleReject(ctx, "read", "fixture", map[string]any{"path": "input"}, cause); got != cause {
		t.Fatal("native owner rejection was rendered before receipt settlement")
	}
	release()
	if memory.held != baseline {
		t.Fatal("decoded observation lease leaked")
	}
	tc.Identity.ToolCallID = "another-model-call"
	if _, _, err := programModelArguments(t.Context(), frame, memory, tc); err == nil || memory.held != baseline {
		t.Fatal("wrong model call received observation or retained state")
	}
}
