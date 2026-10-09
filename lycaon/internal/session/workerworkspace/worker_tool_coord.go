package workerworkspace

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/projectroot"
	sessioncheckpoint "github.com/lycaon/lycaon/internal/session/checkpoint"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// SetCallManager wires inter-agent reservations for sibling worker coordination.
func (m *Service) SetCalls(cm call.CallManager) {
	if m == nil {
		return
	}
	m.calls = cm
}

// EnrichWorkerToolContext binds child-session worker state.
func (m *Service) Enrich(ctx context.Context, sess *api.Session, tctx tools.ToolContext) (tools.ToolContext, error) {
	if m == nil || sess == nil {
		return tctx, nil
	}
	parentID := strings.TrimSpace(sess.ParentSessionID)
	if parentID == "" || m.tasks == nil {
		return tctx, nil
	}
	jobID := strings.TrimSpace(tctx.Identity.WorkerJobID)
	if jobID == "" {
		jobID = workercontext.Job(ctx)
	}
	if jobID == "" {
		return tctx, fmt.Errorf("worker job id required")
	}
	task, ok := m.tasks.Get(jobID)
	if !ok || task == nil {
		return tctx, nil
	}
	if task.EffectiveScope().IsWrite() && strings.TrimSpace(task.WorkspaceRoot) == "" {
		claimed, err := m.tasks.ClaimWorkerBranch(ctx, task.ID)
		if err != nil {
			return tctx, fmt.Errorf("claim worker branch: %w", err)
		}
		if claimed == nil {
			return tctx, fmt.Errorf("claim worker branch returned no task")
		}
		task = claimed
	}
	tctx.Identity.WorkerJobID = task.ID
	// A worker addresses its own workspace only once it has a private branch;
	// a read-scoped worker reads the project tree and its open documents.
	tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	tctx.Identity.ParentSessionID = parentID
	if tctx.Identity.RootSessionID == "" && m.store != nil {
		tctx.Identity.RootSessionID = sessiontree.RootID(ctx, m.store, sess.ID)
	}
	if tctx.Identity.RootSessionID == "" {
		tctx.Identity.RootSessionID = parentID
	}
	tctx.Identity.HandoffSessionID = parentID
	tctx.Identity.HandoffAgentID = task.ID
	tctx.Source.WorkerCoord = m
	if root := strings.TrimSpace(task.WorkspaceRoot); root != "" && task.EffectiveScope().IsWrite() {
		layout, err := workspace.LoadBranchLayout(root)
		if err != nil {
			tctx.Source.BranchWorkspace = nil
			return tctx, err
		}
		tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
		tctx.Source.WorkerSourceRoots = workerSourceRootPaths(layout.Roots)
		tctx.Source.Roots = layout.Roots
		tctx.Source.WorkerBranchRoot = root
		branchState, err := m.branchState(root)
		if err != nil {
			return tctx, err
		}
		tctx.Source.BranchWorkspace = branchState
	}
	return tctx, nil
}

func (m *Service) BoardRoots(ctx context.Context, sess *api.Session) ([]projectroot.RootRef, error) {
	roots, err := m.workspace.Roots(ctx, sess)
	if err != nil {
		return nil, err
	}
	tctx, err := m.Enrich(ctx, sess, tools.ToolContext{Source: tools.InvocationSource{Roots: roots, ActiveRootID: sess.WorkspaceRootID}})
	if err != nil {
		return nil, err
	}
	return tctx.Source.Roots, nil
}

// BeforeWorkerWrite rejects read-scoped worker file mutations and records live touches.
func (m *Service) BeforeWorkerWrite(ctx context.Context, tctx tools.ToolContext, relPath string) error {
	if m == nil || strings.TrimSpace(tctx.Identity.WorkerJobID) == "" {
		return nil
	}
	relPath = sessioncheckpoint.NormalizePath(relPath)
	if relPath == "" {
		return nil
	}
	if m.tasks != nil {
		if task, ok := m.tasks.Get(tctx.Identity.WorkerJobID); ok && task != nil {
			if scope := task.EffectiveScope(); !scope.IsWrite() {
				return &toolrejection.ToolReject{
					Code: TaskScopeReadMutationDeniedCode,
					Data: map[string]any{
						"path":       relPath,
						"agent_type": strings.TrimSpace(task.AgentType),
						"scope_mode": string(scope.Mode),
						"job_id":     strings.TrimSpace(task.ID),
					},
				}
			}
		}
	}
	if m.Touches != nil {
		m.Touches.RecordTouch(tctx.Identity.WorkerJobID, relPath)
	}
	sessionID := strings.TrimSpace(tctx.Identity.HandoffSessionID)
	agent := strings.TrimSpace(tctx.Identity.HandoffAgentID)
	if sessionID == "" || agent == "" || m.calls == nil {
		return nil
	}
	_, err := m.calls.Reserve(ctx, sessionID, []string{relPath}, agent)
	return err
}

// AfterWorkerWrite refreshes the board so pulse lines show live touches.
func (m *Service) AfterWorkerWrite(ctx context.Context, tctx tools.ToolContext, _ string) {
	if m == nil || m.events == nil || strings.TrimSpace(tctx.Identity.ProjectID) == "" {
		return
	}
	m.events.PublishBoard(ctx, strings.TrimSpace(tctx.Identity.ProjectID), strings.TrimSpace(tctx.Identity.HandoffSessionID))
}

// EnsureWorkerBranch ensures a write worker has an isolated branch.
func (m *Service) EnsureBranch(ctx context.Context, tctx tools.ToolContext) (tools.ToolContext, error) {
	if m == nil {
		return tctx, nil
	}
	if strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "" {
		if len(tctx.Source.WorkerSourceRoots) == 0 {
			layout, err := workspace.LoadBranchLayout(tctx.Source.WorkerBranchRoot)
			if err != nil {
				return tctx, err
			}
			tctx.Source.WorkerSourceRoots = workerSourceRootPaths(layout.Roots)
			if len(tctx.Source.Roots) == 0 {
				tctx.Source.Roots = layout.Roots
			}
		}
		if err := tools.RequireBranchWorkspace(tctx); err != nil {
			return tctx, err
		}
		return tctx, nil
	}
	jobID := strings.TrimSpace(tctx.Identity.WorkerJobID)
	if jobID == "" {
		return tctx, nil
	}
	if m.tasks == nil {
		return tctx, fmt.Errorf("worker branch claim not available")
	}
	task, err := m.tasks.ClaimWorkerBranch(ctx, jobID)
	if err != nil {
		return tctx, err
	}
	if task == nil {
		return tctx, fmt.Errorf("worker branch claim returned no task")
	}
	if !task.EffectiveScope().IsWrite() {
		// A read-scoped worker stays on the project tree and its documents.
		tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
		return tctx, nil
	}
	root := strings.TrimSpace(task.WorkspaceRoot)
	if root == "" {
		return tctx, fmt.Errorf("worker branch claim returned empty root")
	}
	tctx.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	tctx.Source.WorkerBranchRoot = root
	layout, err := workspace.LoadBranchLayout(root)
	if err != nil {
		return tctx, err
	}
	tctx.Source.WorkerSourceRoots = workerSourceRootPaths(layout.Roots)
	tctx.Source.Roots = layout.Roots
	branchState, err := m.branchState(root)
	if err != nil {
		return tctx, err
	}
	tctx.Source.BranchWorkspace = branchState
	return tctx, nil
}

func workerSourceRootPaths(roots []projectroot.RootRef) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if path := strings.TrimSpace(root.Path); path != "" {
			out = append(out, path)
		}
	}
	return out
}

// ReleaseReservations clears sibling reservations and touch ledger rows after promote.
func (m *Service) ReleaseReservations(ctx context.Context, parentSessionID, jobID string) error {
	parentSessionID = strings.TrimSpace(parentSessionID)
	jobID = strings.TrimSpace(jobID)
	if parentSessionID == "" || jobID == "" {
		return nil
	}
	if m.Touches != nil {
		m.Touches.ClearJob(jobID)
	}
	if m.calls == nil {
		return nil
	}
	return m.calls.ReleaseAll(ctx, parentSessionID, jobID)
}
