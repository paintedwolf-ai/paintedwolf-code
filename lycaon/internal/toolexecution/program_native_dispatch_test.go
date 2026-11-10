package toolexecution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
	for _, tool := range []string{"command", "verify"} {
		t.Run(tool, func(t *testing.T) { nativeReadAdmissionFixture(t, "input", tool) })
	}
}

func nativeReadAdmissionFixture(t *testing.T, inputPath, modelTool string) {
	t.Helper()
	parent, tc := frameFixture(t)
	root := tc.ActiveRootPath()
	testutil.FailErr(t, "seed original", os.WriteFile(filepath.Join(root, inputPath), []byte("original bytes"), 0600))
	testutil.FailErr(t, "seed changed", os.WriteFile(filepath.Join(root, "changed"), []byte("wrong bytes"), 0600))
	classes, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
	// Both real preinvoke anchors must see the submitted command envelope,
	// independently of the raw native path handed to the existing read door.
	var observationRules []*oar.Rule
	for _, anchor := range []string{oar.AnchorToolPreInvoke, oar.AnchorToolHandler} {
		observationRules = append(observationRules, &oar.Rule{ID: anchor, Kind: oar.KindPolicy, Anchor: anchor, Effect: oar.EffectBlock, Enforcement: "enforce", When: `tool != "` + modelTool + `" || paintedwolf.native_operation != "read" || tool_arg_string("command") != "fixture" || tool_arg_string("path") != ""`})
	}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet(observationRules), nil, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	pipeline.EnableAnchor(oar.AnchorToolHandler)
	executor.Rejections.SetBlockPlane(&toolfeedback.BlockPlane{Pipeline: pipeline})
	definition := nativeDefinition(t, "read")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "freeze actual profile", err)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: modelTool, Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "freeze frame", err)
	t.Cleanup(frame.Close)
	child := parent
	child.Identity.ExecutionID = "child"
	child.Identity.ParentExecutionID = "parent"
	bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind native read", err)
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", owner.Set(parent.SessionID, "- [ ] Inspect"))
	args := map[string]any{"path": inputPath}
	prepared, invoked := false, false
	var stdout bytes.Buffer
	effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", inputPath}}, Arguments: args, Streams: commandprogram.Streams{Output: &stdout}}
	effect.PrepareReviewed = func(ctx context.Context, resolved map[string]any, actual tools.ToolContext) (PreparedNativeEffect, error) {
		args["path"] = "changed"
		path := resolved["path"].(string)
		paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, ".")
		if err != nil {
			return PreparedNativeEffect{}, err
		}
		nativePath, err := paths.Bind(ctx, path, false)
		if err != nil {
			return PreparedNativeEffect{}, err
		}
		prepared = true
		return PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, actual tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			invoked = true
			file, err := nativePath.Open()
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "original bytes" {
				t.Fatalf("executed unreviewed input %q: %v", data, err)
			}
			if _, err := streams.Output.Write(data); err != nil {
				return toolcontract.ProducerResult{}, err
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}
	starts := 0
	observer := nativeStartFunc(func(_ context.Context, actual toolcontract.BoundOperation, call string, review toolcontract.ReviewedOperationInputs) error {
		decoded, release, decodeErr := review.Arguments(t.Context(), stageMemory(t, 64<<20))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		defer release()
		if decoded["path"] != inputPath || len(review.ArgumentsDigest()) != 64 {
			t.Fatalf("unfrozen review: %+v", review)
		}
		if !prepared || invoked || actual.Invocation().Identity.ExecutionID != "child" || call != "actual-model-call" {
			t.Fatal("start marker ordering/identity changed")
		}
		starts++
		return nil
	})
	admission := stageMemory(t, 64<<20)
	result, err := executor.InvokeNative(t.Context(), frame, surface, admission, nativeProgressFixture{owner}, observer, effect, tc)
	testutil.FailErr(t, "invoke actual native read binding", err)
	if !result.OwnerInvoked || !result.OutputPublishable || result.Producer.EffectCertainty != toolcontract.NoEffect || starts != 1 || admission.InUse().State != 0 || stdout.String() != "original bytes" {
		t.Fatalf("lost invoked/read distinction or reservation: %+v held%d", result, admission.InUse().State)
	}
	invoked = false
	denial := errors.New("start record refused")
	result, err = executor.InvokeNative(t.Context(), frame, surface, stageMemory(t, 64<<20), nativeProgressFixture{owner}, nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
		return denial
	}), effect, tc)
	if !errors.Is(err, denial) || result.OwnerInvoked || invoked {
		t.Fatalf("owner dispatched without start record: %+v %v", result, err)
	}
	result, err = executor.InvokeNative(t.Context(), frame, surface, stageMemory(t, 64<<20), nativeProgressFixture{owner}, nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
		frame.Close()
		return nil
	}), effect, tc)
	if err == nil || result.OwnerInvoked || invoked {
		t.Fatalf("owner dispatched after frame closed at marker: %+v %v", result, err)
	}
}

func TestNativeOwnerRevalidatesLiveDenyAfterStartRecorder(t *testing.T) {
	parent, tc := frameFixture(t)
	root := tc.ActiveRootPath()
	testutil.FailErr(t, "seed read target", os.WriteFile(filepath.Join(root, "input"), []byte("authorized bytes"), 0600))
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create live policy store", err)
	// Quiet discretionary policy must still enforce an explicit user deny.
	neverAsk := true
	testutil.FailErr(t, "configure quiet policy", store.PutGlobal(settings.ApprovalConfig{NeverAsk: &neverAsk}))
	classes, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := NewExecutor(NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), settings.NewRuleApprovalGate(store, settings.NoSources())), tools.NewDefaultRegistry(), "fixture")
	definition := nativeDefinition(t, "read")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "freeze native surface", err)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat input"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "capture native frame", err)
	t.Cleanup(frame.Close)
	progressStore := progress.NewMemoryStore()
	testutil.FailErr(t, "set current progress", progressStore.Set(parent.SessionID, "- [ ] Inspect"))
	for _, phase := range []string{"allowed", "late-deny", "recovery"} {
		t.Run(phase, func(t *testing.T) {
			testutil.FailErr(t, "clear only fixture policy", store.PutProject(root, settings.ApprovalConfig{}))
			child := parent
			child.Identity.ExecutionID, child.Identity.ParentExecutionID = phase, parent.Identity.ExecutionID
			bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
			testutil.FailErr(t, "bind fresh native read", err)
			var output bytes.Buffer
			entered, recorded := false, false
			effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: phase, OperationID: "read", Argv: []string{"cat", "input"}}, Arguments: map[string]any{"path": "input"}, Streams: commandprogram.Streams{Output: &output}}
			effect.PrepareReviewed = func(ctx context.Context, args map[string]any, actual tools.ToolContext) (PreparedNativeEffect, error) {
				paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, ".")
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				path, err := paths.Bind(ctx, args["path"].(string), false)
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				return PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, actual tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					entered = true
					file, err := path.Open()
					if err != nil {
						return toolcontract.ProducerResult{}, err
					}
					defer file.Close()
					_, err = io.Copy(streams.Output, file)
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, err
				}}, nil
			}
			observer := nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
				recorded = true
				if phase == "late-deny" {
					return store.PutProject(root, settings.ApprovalConfig{Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "read", Effect: settings.ApprovalEffectDeny}}})
				}
				return nil
			})
			memory := stageMemory(t, 64<<20)
			result, err := executor.InvokeNative(t.Context(), frame, surface, memory, nativeProgressFixture{progressStore}, observer, effect, tc)
			if !recorded || memory.InUse().State != 0 {
				t.Fatalf("fixture missed recorder or retained invocation state: recorded=%t state=%d", recorded, memory.InUse().State)
			}
			if phase == "late-deny" {
				reject := toolrejection.AsToolReject(err)
				if reject == nil || reject.Code != "APPROVAL_RULE_DENIED" || entered || result.OwnerInvoked || result.OutputPublishable || output.Len() != 0 {
					t.Fatalf("late deny reached native owner: err=%v entered=%t result=%+v output=%q", err, entered, result, output.String())
				}
				return
			}
			testutil.FailErr(t, "invoke authorized native read", err)
			if !entered || !result.OwnerInvoked || !result.OutputPublishable || output.String() != "authorized bytes" {
				t.Fatalf("authorized workflow failed: entered=%t result=%+v output=%q", entered, result, output.String())
			}
		})
	}
}
