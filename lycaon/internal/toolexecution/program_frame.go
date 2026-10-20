package toolexecution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramFrame pins attribution and addressed trees from the host invocation.
// It carries no grants: people, confinement, revision and content-review owners
// must still admit each reached effect. ActorID comes from the host's captured
// invocation, never curator/debug metadata or a model-supplied argument.
type ProgramFrame struct {
	parent     toolcontract.OperationInvocation
	identity   tools.InvocationIdentity
	roots      map[string]programRoot
	workerRoot string
	surfaceID  string
}

type programRoot struct {
	root   projectroot.RootRef
	branch sourcebranch.ID
}

// FreezeProgramFrame captures immutable attribution before any child starts.
// The host resolves its actor and project/session identity before calling this.
func FreezeProgramFrame(parent toolcontract.OperationInvocation, tc tools.ToolContext) (ProgramFrame, error) {
	if err := parent.Validate(); err != nil {
		return ProgramFrame{}, err
	}
	if parent.Kind != toolcontract.ProgramChildInvocation || tc.Identity.ToolCallID == "" || parent.SessionID != tc.Identity.SessionID || parent.Scope.ProjectID != tc.Identity.ProjectID {
		return ProgramFrame{}, fmt.Errorf("program frame requires matching host session, project and initiating call")
	}
	frame := ProgramFrame{parent: parent, identity: tc.Identity, roots: map[string]programRoot{}, workerRoot: tc.Source.WorkerBranchRoot, surfaceID: tc.Turn.TurnSurfaceID}
	for _, root := range tc.Source.Roots {
		if root.ID == "" || root.Path == "" {
			return ProgramFrame{}, fmt.Errorf("program frame requires addressed root identity")
		}
		if _, exists := frame.roots[root.ID]; exists {
			return ProgramFrame{}, fmt.Errorf("duplicate addressed program root")
		}
		branch, err := tc.SourceBranch(root.ID)
		if err != nil {
			return ProgramFrame{}, err
		}
		frame.roots[root.ID] = programRoot{root: root, branch: branch}
	}
	if err := frame.validateScope(parent.Scope, tc); err != nil {
		return ProgramFrame{}, err
	}
	return frame, nil
}

func (f ProgramFrame) validateScope(scope toolcontract.OperationScope, tc tools.ToolContext) error {
	if scope.Kind != f.parent.Scope.Kind || scope.ProjectID != f.parent.Scope.ProjectID {
		return fmt.Errorf("program scope changed")
	}
	if scope.Kind == toolcontract.SessionOperationScope {
		return nil
	}
	pinned, ok := f.roots[scope.RootID]
	if !ok {
		return fmt.Errorf("program root was not captured")
	}
	branch, err := tc.SourceBranch(scope.RootID)
	if err != nil {
		return err
	}
	if branch != pinned.branch || scope.BranchID != branch.String() {
		return fmt.Errorf("program source branch changed")
	}
	for _, root := range tc.Source.Roots {
		if root.ID == scope.RootID && root == pinned.root {
			return nil
		}
	}
	return fmt.Errorf("program addressed root changed")
}

// ChildContext creates fresh one-call state while retaining the actual outer
// model call for checkpoints and ledger correlation. ExecutionID is a distinct
// host action identity, never placed in ToolCallID. The binder must next perform
// effect admission using the frozen typed inputs before invoking an owner.
func (f ProgramFrame) ChildContext(ctx context.Context, definition toolcontract.OperationDefinition, child toolcontract.OperationInvocation, tc tools.ToolContext) (tools.ToolContext, error) {
	if err := ctx.Err(); err != nil {
		return tools.ToolContext{}, err
	}
	if err := child.Validate(); err != nil {
		return tools.ToolContext{}, err
	}
	if f.parent.Identity.ProgramID == "" || child.Kind != toolcontract.ProgramChildInvocation || child.Identity.ProgramID != f.parent.Identity.ProgramID || child.ActorID != f.parent.ActorID || child.SessionID != f.parent.SessionID || child.CatalogDigest != f.parent.CatalogDigest || tc.Identity != f.identity || tc.Source.WorkerBranchRoot != f.workerRoot || tc.Turn.TurnSurfaceID != f.surfaceID {
		return tools.ToolContext{}, fmt.Errorf("program child attribution changed")
	}
	if err := f.validateScope(child.Scope, tc); err != nil {
		return tools.ToolContext{}, err
	}
	if err := tools.ValidateWorkerBranch(ctx, tc); err != nil {
		return tools.ToolContext{}, err
	}
	if definition.Digest() == "" {
		return tools.ToolContext{}, fmt.Errorf("program child requires frozen operation definition")
	}
	facts := definition.Facts()
	if facts.CatalogDigest != child.CatalogDigest {
		return tools.ToolContext{}, fmt.Errorf("program child catalog changed")
	}
	tc.BeginInvocation()
	tc.Local.LocalListenGranted = false
	tc.Local.LocalListenPorts = nil
	tc.Local.LoopbackConnectGranted = false
	tc.Local.LoopbackConnectPorts = nil
	tc.Local.OwnedLoopbackPorts = nil
	tc.Socket = tools.InvocationSocket{}
	tc.Direct = tools.InvocationDirect{}
	tc.Files.GrantedWriteRoots = nil
	tc.Effects.Out = nil
	tc.Effects.CanonicalArgs = nil
	tc.Effects.RequestedArgs = nil
	tc.Invocation = tools.Invocation{ID: child.Identity.ExecutionID, ProjectID: child.Scope.ProjectID, SessionID: child.SessionID, MessageID: tc.Identity.MessageID, ToolCallID: tc.Identity.ToolCallID, ToolName: facts.Identity, ContractDigest: facts.Contract.Digest(), Contract: facts.Contract}
	return tc, nil
}
