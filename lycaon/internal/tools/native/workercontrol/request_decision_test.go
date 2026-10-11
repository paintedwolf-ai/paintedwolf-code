package workercontrol_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakeDecisionRecorder struct {
	request api.WorkerDecisionRequest
	calls   int
}

func (f *fakeDecisionRecorder) Put(_ context.Context, request api.WorkerDecisionRequest) error {
	f.request = request
	f.calls++
	return nil
}

// Nothing sits above an addressed session to answer, so the refusal points at
// the user rather than parking a job that no coordinator will wake.
func TestRequestDecisionRejectsAddressedSession(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	rec := &fakeDecisionRecorder{}
	testutil.FailErr(t, "register", native.RegisterRequestDecisionTool(reg, workertools.RequestDecisionDeps{Recorder: rec}))
	_, err := reg.Run(context.Background(), workertools.RequestDecisionTool, map[string]any{
		"question": "Refactor or work around?",
		"options":  []any{"refactor", "work around"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s1"},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "REQUEST_DECISION_ADDRESSED_SESSION" {
		t.Fatalf("err = %v want REQUEST_DECISION_ADDRESSED_SESSION", err)
	}
	if rec.calls != 0 {
		t.Fatalf("recorder called %d times from an addressed session", rec.calls)
	}
}

func TestRequestDecisionRecords(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	rec := &fakeDecisionRecorder{}
	testutil.FailErr(t, "register", native.RegisterRequestDecisionTool(reg, workertools.RequestDecisionDeps{Recorder: rec}))
	out, err := reg.Run(context.Background(), "request_decision", map[string]any{
		"question":      "Refactor the shared type or work around it?",
		"options":       []any{"refactor", "  ", "work around"},
		"blocker_class": "sandbox",
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child-1",
			ParentSessionID: "parent-1",
			WorkerJobID:     "job-1"},
	})
	testutil.FailErr(t, "run", err)
	if !strings.Contains(out, "decision_requested") || !strings.Contains(out, `"blocker_class":"sandbox"`) {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, `"question":"Refactor the shared type or work around it?"`) {
		t.Fatalf("structured result missing question: %q", out)
	}
	if rec.calls != 1 || rec.request.ChildSessionID != "child-1" || len(rec.request.Options) != 2 || rec.request.BlockerClass != api.WorkerBlockerSandbox {
		t.Fatalf("recorder = %+v", rec)
	}
}

func TestRequestDecisionDefaultsBlockerClass(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	rec := &fakeDecisionRecorder{}
	testutil.FailErr(t, "register", native.RegisterRequestDecisionTool(reg, workertools.RequestDecisionDeps{Recorder: rec}))
	_, err := reg.Run(context.Background(), "request_decision", map[string]any{
		"question": "q",
		"options":  []any{"a", "b"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child-1",
			ParentSessionID: "parent-1",
			WorkerJobID:     "job-1"},
	})
	testutil.FailErr(t, "run", err)
	if rec.request.BlockerClass != api.WorkerBlockerDecision {
		t.Fatalf("blockerClass = %q", rec.request.BlockerClass)
	}
}

func TestRequestDecisionValidation(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", native.RegisterRequestDecisionTool(reg, workertools.RequestDecisionDeps{Recorder: &fakeDecisionRecorder{}}))
	cases := []struct {
		name string
		args map[string]any
		tctx tools.ToolContext
		want string
	}{
		{"missing question", map[string]any{"options": []any{"a", "b"}}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "c",
				ParentSessionID: "p",
				WorkerJobID:     "j"},
		}, "question"},
		{"too few options", map[string]any{"question": "q", "options": []any{"a"}}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "c",
				ParentSessionID: "p",
				WorkerJobID:     "j"},
		}, "options"},
		{"bad blocker", map[string]any{"question": "q", "options": []any{"a", "b"}, "blocker_class": "npm"}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "c",
				ParentSessionID: "p",
				WorkerJobID:     "j"},
		}, "blocker_class"},
		{"missing worker job", map[string]any{"question": "q", "options": []any{"a", "b"}}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "c",
				ParentSessionID: "p"},
		}, "worker job"},
	}
	for _, tc := range cases {
		_, err := reg.Run(context.Background(), "request_decision", tc.args, tc.tctx)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v want contains %q", tc.name, err, tc.want)
		}
	}
}

func TestDecisionAttachmentsCannotDisappearDuringParsing(t *testing.T) {
	const first = "11111111-1111-4111-8111-111111111111"
	cases := []map[string]any{
		{"artifact_id": nil}, {"artifact_id": ""}, {"artifact_id": 42},
		{"artifact_ids": nil}, {"artifact_ids": "bad"}, {"artifact_ids": []any{}},
		{"artifact_ids": []any{first, 42}}, {"artifact_ids": []any{first, ""}},
		{"artifact_ids": []any{first, "bad"}}, {"artifact_ids": []any{first, first}},
		{"artifact_id": first, "artifact_ids": []any{}},
	}
	for _, attachment := range cases {
		reg := tools.NewDefaultRegistry()
		rec := &fakeDecisionRecorder{}
		testutil.FailErr(t, "register decision", native.RegisterRequestDecisionTool(reg, workertools.RequestDecisionDeps{Recorder: rec}))
		args := map[string]any{"question": "Which design?", "options": []any{"first", "second"}}
		for k, v := range attachment {
			args[k] = v
		}
		_, err := reg.Run(t.Context(), workertools.RequestDecisionTool, args, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "child",
				ParentSessionID: "parent",
				WorkerJobID:     "job"},
		})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || (reject.Code != "REQUEST_DECISION_ARTIFACT_SHAPE" && reject.Code != "REQUEST_DECISION_ARTIFACT_ARITY") || rec.calls != 0 {
			t.Fatalf("malformed attachments were lost or recorded: args=%v err=%v calls=%d", attachment, err, rec.calls)
		}
	}
}
