package toolexecution

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func frameFixture(t *testing.T) (toolcontract.OperationInvocation, tools.ToolContext) {
	t.Helper()
	parent := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "unit", ExecutionID: "parent"}, ActorID: "host-person", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project", RootID: "root"}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{ProjectID: "project", SessionID: "session", ToolCallID: "actual-model-call", Agent: "fixture"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"}}
	return parent, tc
}

func TestProgramFramePreservesModelCallAndClearsChildAuthority(t *testing.T) {
	parent, tc := frameFixture(t)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "capture program frame", err)
	t.Cleanup(frame.Close)
	child := parent
	child.Identity = toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "stage", ExecutionID: "child", ParentExecutionID: "parent"}
	tc.Execution.ProcessControl = true
	tc.Execution.HostExecution = true
	tc.Local.LocalListenGranted = true
	tc.Local.LoopbackConnectGranted = true
	tc.Local.OwnedLoopbackPorts = []uint16{8000}
	tc.Files.GrantedWriteRoots = []string{"fixture-grant"}
	tc.Effects.Out = &tools.ToolInvocationOut{}
	bound, err := frame.ChildContext(t.Context(), nativeDefinition(t, "read"), child, tc)
	testutil.FailErr(t, "bind child attribution", err)
	if bound.Invocation.ID != "child" || bound.Invocation.ToolCallID != "actual-model-call" || bound.Identity.ToolCallID != "actual-model-call" {
		t.Fatalf("child lost separate attribution: %+v", bound.Invocation)
	}
	if bound.Execution.ProcessControl || bound.Execution.HostExecution || bound.Local.LocalListenGranted || bound.Local.LoopbackConnectGranted || len(bound.Local.OwnedLoopbackPorts) > 0 || len(bound.Files.GrantedWriteRoots) > 0 || bound.Effects.Out != nil {
		t.Fatal("child inherited outer one-call authority or result sink")
	}
	if !tc.Execution.HostExecution || tc.Effects.Out == nil {
		t.Fatal("binding changed parent context")
	}
	if tc.EffectActionID() != "actual-model-call" {
		t.Fatal("retained action identity changed")
	}
	definition := nativeDefinition(t, "read")
	operation, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind exact native identity", err)
	bound.Invocation.NativeOperation = &operation
	if bound.EffectActionID() != "child" || bound.Identity.ToolCallID != "actual-model-call" {
		t.Fatal("native effect identity conflated with initiating call")
	}
}

func TestProgramFrameRejectsChangedAttributionAndTrees(t *testing.T) {
	for _, change := range []string{"actor", "session", "program", "call", "root", "branch", "worker"} {
		t.Run(change, func(t *testing.T) {
			parent, tc := frameFixture(t)
			frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
			testutil.FailErr(t, "capture frame", err)
			t.Cleanup(frame.Close)
			child := parent
			child.Identity.ExecutionID = "child"
			child.Identity.ParentExecutionID = "parent"
			switch change {
			case "actor":
				child.ActorID = "other-person"
			case "session":
				child.SessionID = "other-session"
			case "program":
				child.Identity.ProgramID = "other-program"
			case "call":
				tc.Identity.ToolCallID = "other-call"
			case "root":
				tc.Source.Roots[0].Path = t.TempDir()
			case "branch":
				tc.Source.ProjectRootBranches = map[string]sourcebranch.ID{"root": "other-branch"}
			case "worker":
				tc.Source.WorkerBranchRoot = t.TempDir()
			}
			if _, err := frame.ChildContext(t.Context(), nativeDefinition(t, "read"), child, tc); err == nil {
				t.Fatal("changed frame admitted")
			}
		})
	}
	parent, tc := frameFixture(t)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "capture frame", err)
	t.Cleanup(frame.Close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := frame.ChildContext(ctx, nativeDefinition(t, "read"), parent, tc); err == nil {
		t.Fatal("cancelled child admitted")
	}
	if _, err := frame.ChildContext(t.Context(), toolcontract.OperationDefinition{}, parent, tc); err == nil {
		t.Fatal("missing operation admitted")
	}
	if _, err := (ProgramFrame{}).ChildContext(t.Context(), nativeDefinition(t, "read"), parent, tc); err == nil {
		t.Fatal("uncaptured frame admitted")
	}
}

// Exercise the dispatch entry, not just the frame validator: every rejected
// substitution must stop before preparation, recording, or the actual read door.
func TestNativeDispatchBindingSubstitutionsHaveNoEffects(t *testing.T) {
	for _, change := range []string{"authorized", "actor", "session", "program", "call", "root", "branch", "worker", "operation", "stage-execution", "zero-binding", "profile", "canceled", "closed-frame"} {
		t.Run(change, func(t *testing.T) {
			parent, tc := frameFixture(t)
			testutil.FailErr(t, "seed effect target", os.WriteFile(filepath.Join(tc.ActiveRootPath(), "input"), []byte("admitted bytes"), 0600))
			classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
			testutil.FailErr(t, "compile read profile", err)
			boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
			executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
			definition := nativeDefinition(t, "read")
			surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
			testutil.FailErr(t, "freeze current surface", err)
			frameMemory := stageMemory(t, 64<<20)
			frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat input"}}, frameMemory)
			testutil.FailErr(t, "capture frame", err)
			t.Cleanup(frame.Close)
			child := parent
			child.Identity.ExecutionID, child.Identity.ParentExecutionID = "child", parent.Identity.ExecutionID
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch change {
			case "actor":
				child.ActorID = "different-person"
			case "session":
				child.SessionID = "different-session"
			case "program":
				child.Identity.ProgramID = "different-program"
			case "call":
				tc.Identity.ToolCallID = "different-call"
			case "root":
				tc.Source.Roots[0].Path = t.TempDir()
			case "branch":
				tc.Source.ProjectRootBranches = map[string]sourcebranch.ID{"root": "different-branch"}
			case "worker":
				tc.Source.WorkerBranchRoot = t.TempDir()
			case "profile":
				tc.Identity.Agent = "different-profile"
			case "canceled":
				cancel()
			case "closed-frame":
				frame.Close()
			}
			bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
			testutil.FailErr(t, "bind structurally valid operation", err)
			var output bytes.Buffer
			prepared, recorded, opened := false, false, false
			effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", "input"}}, Arguments: map[string]any{"path": "input"}, Streams: commandprogram.Streams{Output: &output}}
			effect.PrepareReviewed = func(ctx context.Context, args map[string]any, actual tools.ToolContext) (PreparedNativeEffect, error) {
				prepared = true
				paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, ".")
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				path, err := paths.Bind(ctx, args["path"].(string), false)
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				return PreparedNativeEffect{Operation: bound, InvokeOwner: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					opened = true
					file, err := path.Open()
					if err != nil {
						return toolcontract.ProducerResult{}, err
					}
					defer file.Close()
					_, err = io.Copy(streams.Output, file)
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, err
				}}, nil
			}
			switch change {
			case "operation":
				effect.Stage.OperationID = "write"
			case "stage-execution":
				effect.Stage.ExecutionID = "different-execution"
			case "zero-binding":
				effect.Operation = toolcontract.BoundOperation{}
			}
			store := progress.NewMemoryStore()
			testutil.FailErr(t, "seed progress", store.Set(parent.SessionID, "- [ ] Inspect"))
			observer := nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
				recorded = true
				return nil
			})
			memory := stageMemory(t, 64<<20)
			result, err := executor.InvokeNative(ctx, frame, surface, memory, nativeProgressFixture{store}, observer, effect, tc)
			frame.Close()
			if memory.InUse().State != 0 || frameMemory.InUse().State != 0 {
				t.Fatal("dispatch retained invocation or frame state")
			}
			if change == "authorized" {
				testutil.FailErr(t, "dispatch authorized read", err)
				if !prepared || !recorded || !opened || !result.OwnerInvoked || !result.OutputPublishable || output.String() != "admitted bytes" {
					t.Fatalf("positive effect door did not execute: prepared=%t recorded=%t opened=%t result=%+v output=%q", prepared, recorded, opened, result, output.String())
				}
			} else if err == nil || prepared || recorded || opened || result.OwnerInvoked || result.OutputPublishable || output.Len() != 0 {
				t.Fatalf("substituted binding reached effect: err=%v prepared=%t recorded=%t opened=%t result=%+v output=%q", err, prepared, recorded, opened, result, output.String())
			}
		})
	}
}
