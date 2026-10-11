package delegation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/pkg/api"
)

var errWorkerNotDelegated = errors.New("worker task is not a delegation leg")

// WorkflowRunPhaseResolver supplies current workflow phase for a run id.
type WorkflowRunPhaseResolver interface {
	PhaseForRun(ctx context.Context, workflowRunID string) (phaseID string, err error)
}

// AgentsMDChainBuilder renders path-scoped AGENTS.md chain content for worker turns.
type AgentsMDChainBuilder func(ctx context.Context, sessionID, workspacePath string, paths []string) (api.Message, error)

type WorkerContextTasks interface {
	GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool)
}

// WorkerContextLoader builds worker leg context from its recorded job binding.
type WorkerContextLoader struct {
	Store         Store
	Tasks         WorkerContextTasks
	Runs          WorkflowRunPhaseResolver
	MatcherFor    func(*api.Session) PlaybookMatcherInterface
	AgentsFor     func(*api.Session) profiles.AgentProfileResolver
	Topology      func(workflowID string) string
	Tools         LegToolLister
	Scans         scan.WorkerScanLister
	AgentsMDChain AgentsMDChainBuilder
	Repo          repoinfo.Provider
}

func (l *WorkerContextLoader) BuildWorkerPromptContext(childID string, sess *api.Session) (inject.WorkerLegContext, error) {
	out := inject.WorkerLegContext{}
	if l == nil || sess == nil || strings.TrimSpace(sess.ParentSessionID) == "" {
		return out, fmt.Errorf("not a worker child session")
	}
	delegation, leg, err := l.workerLeg(childID, sess)
	if err != nil {
		return out, err
	}

	agentType := strings.TrimSpace(sess.AgentType)
	if agentType == "" {
		agentType = strings.TrimSpace(leg.AgentType)
	}
	out = inject.WorkerLegContext{
		LegID:              leg.ID,
		AgentType:          agentType,
		WorkflowID:         strings.TrimSpace(delegation.WorkflowID),
		CompletionCriteria: append([]string(nil), leg.CompletionCriteria...),
		RequiresIsolation:  strings.TrimSpace(leg.WorkspaceRoot) != "",
	}
	if l.Topology != nil {
		out.TopologyPattern = strings.TrimSpace(l.Topology(out.WorkflowID))
	}
	if out.TopologyPattern == "" {
		out.TopologyPattern = "pipeline"
	}
	if l.Runs != nil && strings.TrimSpace(delegation.WorkflowRunID) != "" {
		if phase, err := l.Runs.PhaseForRun(context.Background(), delegation.WorkflowRunID); err == nil {
			out.PhaseID = strings.TrimSpace(phase)
		}
	}
	var agents profiles.AgentProfileResolver
	if l.AgentsFor != nil {
		agents = l.AgentsFor(sess)
	}
	var matcher PlaybookMatcherInterface
	if l.MatcherFor != nil {
		matcher = l.MatcherFor(sess)
	}
	out.LegTools = resolveLegTools(context.Background(), l.Tools, agents, sess, agentType)
	if matcher != nil && agentType != "" {
		checklist, err := matcher.MatchForAgent(agentType, out.TopologyPattern, out.PhaseID)
		if err != nil {
			return out, err
		}
		out.Checklist = checklist
	}
	out, err = attachWorkerScanDigest(out, sess.WorkspacePath, delegation.WorkflowRunID, l.Scans)
	if err != nil {
		return out, err
	}
	out.LayoutTopLevel = layoutTopLevelForWorker(context.Background(), l.Repo, sess.WorkspacePath)
	if block, err := l.attachAgentsMDChain(context.Background(), sess, leg.Files); err != nil {
		return out, err
	} else if strings.TrimSpace(block.Content) != "" {
		out.AgentsMDMessage = block
	}
	return inject.BuildWorkerPromptContext(out), nil
}

func (l *WorkerContextLoader) workerLeg(childID string, sess *api.Session) (*api.Delegation, *api.Leg, error) {
	if l.Store == nil {
		return nil, nil, fmt.Errorf("delegation store not configured")
	}
	if l.Tasks == nil {
		return nil, nil, fmt.Errorf("worker task lookup not configured")
	}
	task, ok := l.Tasks.GetLatestByChildSessionID(context.Background(), childID)
	if !ok || task == nil {
		return nil, nil, fmt.Errorf("worker task not found for child %q", childID)
	}
	if childID != sess.ID || task.ChildSessionID != childID || task.ParentSessionID != sess.ParentSessionID || task.AgentType != sess.AgentType {
		return nil, nil, fmt.Errorf("worker task %q does not match child session %q", task.ID, childID)
	}
	if task.DelegationID == "" && task.LegID == "" {
		return nil, nil, errWorkerNotDelegated
	}
	if task.DelegationID == "" || task.LegID == "" {
		return nil, nil, fmt.Errorf("worker task %q has an incomplete delegation binding", task.ID)
	}
	delegation, err := l.Store.Get(context.Background(), task.DelegationID)
	if err != nil {
		return nil, nil, err
	}
	if delegation == nil {
		return nil, nil, fmt.Errorf("delegation %q not found", task.DelegationID)
	}
	parent, ok := l.Store.SessionID(task.DelegationID)
	if !ok || parent != task.ParentSessionID {
		return nil, nil, fmt.Errorf("delegation %q does not match worker parent", task.DelegationID)
	}
	leg := findWorkerLeg(delegation.Legs, task)
	if leg == nil {
		return nil, nil, fmt.Errorf("worker task %q has no matching active leg %q", task.ID, task.LegID)
	}

	return delegation, leg, nil
}

func (l *WorkerContextLoader) attachAgentsMDChain(ctx context.Context, sess *api.Session, paths []string) (api.Message, error) {
	if l == nil || l.AgentsMDChain == nil || sess == nil {
		return api.Message{}, nil
	}
	return l.AgentsMDChain(ctx, sess.ID, sess.WorkspacePath, paths)
}

func layoutTopLevelForWorker(ctx context.Context, repo repoinfo.Provider, workspacePath string) []string {
	workspacePath = strings.TrimSpace(workspacePath)
	if repo == nil || workspacePath == "" {
		return nil
	}
	brief, err := repo.Brief(ctx, workspacePath)
	if err != nil || brief == nil {
		return nil
	}
	if len(brief.Layout.TopLevel) > 0 {
		return append([]string(nil), brief.Layout.TopLevel...)
	}
	if len(brief.Layout.Files) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	for _, f := range brief.Layout.Files {
		parts := strings.SplitN(strings.ReplaceAll(f, "\\", "/"), "/", 2)
		name := parts[0]
		if len(parts) > 1 {
			name += "/"
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func findWorkerLeg(legs []api.Leg, task *api.WorkerTask) *api.Leg {
	for i := range legs {
		leg := &legs[i]
		if leg.ID == task.LegID && leg.WorkerID == task.ID && leg.AgentType == task.AgentType &&
			(leg.Status == api.LegStatusDispatched || leg.Status == api.LegStatusRunning) {
			return leg
		}
	}
	return nil
}

var _ assembly.WorkerContextBuilder = (*WorkerContextLoader)(nil)
