package toolexecution

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

func frameFixture(t *testing.T) (toolcontract.OperationInvocation, tools.ToolContext) {
	t.Helper()
	parent := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "unit", ExecutionID: "parent"}, ActorID: "host-person", SessionID: "session", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "project", RootID: "root"}, SourceUnitID: "source", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{ProjectID: "project", SessionID: "session", ToolCallID: "actual-model-call", Agent: "fixture"}, Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: t.TempDir(), IsPrimary: true}}, ActiveRootID: "root"}}
	return parent, tc
}

func TestProgramFramePreservesModelCallAndClearsChildAuthority(t *testing.T) {
	parent, tc := frameFixture(t)
	frame, err := FreezeProgramFrame(parent, tc)
	testutil.FailErr(t, "capture program frame", err)
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
			frame, err := FreezeProgramFrame(parent, tc)
			testutil.FailErr(t, "capture frame", err)
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
	frame, err := FreezeProgramFrame(parent, tc)
	testutil.FailErr(t, "capture frame", err)
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
