package toolexecution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type nativeProgressFixture struct{ store *progress.MemoryStore }

func (p nativeProgressFixture) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type nativeStartFunc func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error

func (f nativeStartFunc) BeforeNativeOwner(ctx context.Context, b toolcontract.BoundOperation, call string, review toolcontract.ReviewedOperationInputs) error {
	return f(ctx, b, call, review)
}

func TestNativeEntryFreezesInputsUsesActualReadDoorAndRecordsInvokedNoMutation(t *testing.T) {
	parent, tc := frameFixture(t)
	root := tc.ActiveRootPath()
	testutil.FailErr(t, "seed original", os.WriteFile(filepath.Join(root, "input"), []byte("original bytes"), 0600))
	testutil.FailErr(t, "seed changed", os.WriteFile(filepath.Join(root, "changed"), []byte("wrong bytes"), 0600))
	classes, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
	definition := nativeDefinition(t, "read")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "freeze actual profile", err)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"script": "fixture"}}, &stageAdmissionFixture{})
	testutil.FailErr(t, "freeze frame", err)
	t.Cleanup(frame.Close)
	child := parent
	child.Identity.ExecutionID = "child"
	child.Identity.ParentExecutionID = "parent"
	bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind native read", err)
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", owner.Set(parent.SessionID, "- [ ] Inspect"))
	args := map[string]any{"path": "input"}
	prepared, invoked := false, false
	effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", "input"}}, Arguments: args}
	effect.PrepareReviewed = func(_ context.Context, resolved map[string]any, _ tools.ToolContext) (PreparedNativeEffect, error) {
		args["path"] = "changed"
		path := resolved["path"].(string)
		prepared = true
		return PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, actual tools.ToolContext) (toolcontract.ProducerResult, error) {
			invoked = true
			reads := projectpaths.NewReadSession(boundary, actual)
			defer reads.Close()
			resolved, err := reads.Resolve(ctx, path)
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			file, err := reads.Open(resolved)
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "original bytes" {
				t.Fatalf("executed unreviewed input %q: %v", data, err)
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}
	starts := 0
	observer := nativeStartFunc(func(_ context.Context, actual toolcontract.BoundOperation, call string, review toolcontract.ReviewedOperationInputs) error {
		if review.CanonicalArguments != `{"path":"input"}` || len(review.ArgumentsDigest) != 64 {
			t.Fatalf("unfrozen review: %+v", review)
		}
		if !prepared || invoked || actual.Invocation().Identity.ExecutionID != "child" || call != "actual-model-call" {
			t.Fatal("start marker ordering/identity changed")
		}
		starts++
		return nil
	})
	admission := &stageAdmissionFixture{}
	result, err := executor.InvokeNative(t.Context(), frame, surface, admission, nativeProgressFixture{owner}, observer, effect, tc)
	testutil.FailErr(t, "invoke actual native read binding", err)
	if !result.OwnerInvoked || !result.OutputPublishable || result.Producer.EffectCertainty != toolcontract.NoEffect || starts != 1 || admission.held != 0 {
		t.Fatalf("lost invoked/read distinction or reservation: %+v held%d", result, admission.held)
	}
	invoked = false
	denial := errors.New("start record refused")
	result, err = executor.InvokeNative(t.Context(), frame, surface, &stageAdmissionFixture{}, nativeProgressFixture{owner}, nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
		return denial
	}), effect, tc)
	if !errors.Is(err, denial) || result.OwnerInvoked || invoked {
		t.Fatalf("owner dispatched without start record: %+v %v", result, err)
	}
}
