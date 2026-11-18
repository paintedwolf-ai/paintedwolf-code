package workercontext

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkflowRunPhaseResolver supplies the active phase for a workflow run ID.
type WorkflowRunPhaseResolver interface {
	PhaseForRun(ctx context.Context, workflowRunID string) (phaseID string, err error)
}

// AgentsMDChainBuilder renders path-scoped AGENTS.md chain content for worker turns.
type AgentsMDChainBuilder func(ctx context.Context, sessionID, workspacePath string, paths []string) (api.Message, error)

// WorkerContextTasks looks up the latest worker task bound to a child session.
type WorkerContextTasks interface {
	GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool)
}

// CompositeWorkerContext assembles worker prompt context from worker records,
// bound task jobs, and workflow run metadata.
type CompositeWorkerContext struct {
	Records          *workerrecords.Store
	Tasks            WorkerContextTasks
	Runs             WorkflowRunPhaseResolver
	Tools            LegToolLister
	Scans            scan.WorkerScanLister
	AgentsMDChain    AgentsMDChainBuilder
	Repo             repoinfo.Provider
	Topology         func(workflowID string) string
	AgentsFor        func(*api.Session) profiles.AgentProfileResolver
	MatcherFor       func(*api.Session) PlaybookMatcherInterface
	WorkflowIDForRun func(ctx context.Context, runID string) string
	ReviewSubject    func(ctx context.Context, jobID string) (worker, handle string, ok bool, err error)
}

func (c *CompositeWorkerContext) BuildWorkerPromptContext(ctx context.Context, childID string, sess *api.Session) (inject.WorkerLegContext, error) {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) == "" {
		return inject.WorkerLegContext{}, fmt.Errorf("not a worker child session")
	}
	if strings.TrimSpace(childID) != "" && childID != sess.ID {
		return inject.WorkerLegContext{}, fmt.Errorf("worker session %q does not match child %q", sess.ID, childID)
	}
	agentType := strings.TrimSpace(sess.AgentType)
	if agentType == "" {
		return inject.WorkerLegContext{}, fmt.Errorf("worker child session missing agent_type")
	}
	out := inject.WorkerLegContext{
		AgentType:       agentType,
		TopologyPattern: "pipeline",
	}
	var agents profiles.AgentProfileResolver
	var lister LegToolLister
	var matcher PlaybookMatcherInterface
	if c != nil {
		lister = c.Tools
		if c.AgentsFor != nil {
			agents = c.AgentsFor(sess)
		}
		if c.MatcherFor != nil {
			matcher = c.MatcherFor(sess)
		}
	}
	out.LegTools = resolveLegTools(ctx, lister, agents, sess, agentType)

	var legFiles []string
	runID := ""
	if c != nil && c.Records != nil {
		actor := workerrecords.Actor{SessionID: sess.ID, Role: workerrecords.RoleWorker, PrincipalID: sess.ID}
		if w, wErr := c.Records.Self(ctx, actor); wErr == nil {
			out.LegID = w.WorkflowWorkID
			if out.LegID == "" {
				out.LegID = w.Name
			}
			out.PhaseID = w.WorkflowPhase
			out.CompletionCriteria = append([]string(nil), w.Criteria...)
			out.RequiresIsolation = w.ScopeMode == workerrecords.ScopeWrite
			legFiles = append([]string(nil), w.Files...)
			runID = w.WorkflowRunID
		}
	}
	if c != nil && c.Tasks != nil {
		if task, ok := c.Tasks.GetLatestByChildSessionID(ctx, sess.ID); ok && task != nil {
			if task.ChildSessionID != sess.ID || task.ParentSessionID != sess.ParentSessionID {
				return inject.WorkerLegContext{}, fmt.Errorf("worker task %q does not match child session %q", task.ID, sess.ID)
			}
			if runID == "" {
				runID = task.WorkflowRunID
			}
			if out.PhaseID == "" {
				out.PhaseID = task.WorkflowPhase
			}
			if len(legFiles) == 0 {
				legFiles = append([]string(nil), task.Files...)
			}
			if !out.RequiresIsolation && task.Scope != nil && task.Scope.Mode == api.TaskScopeModeWrite {
				out.RequiresIsolation = true
			}
			if c.ReviewSubject != nil {
				worker, handle, reviewer, err := c.ReviewSubject(ctx, task.ID)
				if err != nil {
					return out, err
				}
				if reviewer {
					out.ReviewHandle, out.ReviewWorker = handle, worker
				}
			}
		}
	}
	if c != nil && runID != "" {
		if c.WorkflowIDForRun != nil {
			out.WorkflowID = strings.TrimSpace(c.WorkflowIDForRun(ctx, runID))
		}
		if c.Topology != nil && out.WorkflowID != "" {
			if tp := strings.TrimSpace(c.Topology(out.WorkflowID)); tp != "" {
				out.TopologyPattern = tp
			}
		}
		if out.PhaseID == "" && c.Runs != nil {
			if phase, pErr := c.Runs.PhaseForRun(ctx, runID); pErr == nil {
				out.PhaseID = strings.TrimSpace(phase)
			}
		}
	}
	if c != nil {
		out.LayoutTopLevel = layoutTopLevelForWorker(ctx, c.Repo, sess.WorkspacePath)
	}
	if checklist, err := mergeWorkerChecklist(matcher, agentType, out.TopologyPattern, out.PhaseID); err != nil {
		return out, err
	} else if len(checklist) > 0 {
		out.Checklist = checklist
	}
	var scans scan.WorkerScanLister
	if c != nil {
		scans = c.Scans
	}
	var err error
	out, err = attachWorkerScanDigest(ctx, out, sess.WorkspacePath, runID, scans)
	if err != nil {
		return out, err
	}
	if c != nil && c.AgentsMDChain != nil {
		block, err := c.AgentsMDChain(ctx, sess.ID, sess.WorkspacePath, legFiles)
		if err != nil {
			return out, err
		}
		if strings.TrimSpace(block.Content) != "" {
			out.AgentsMDMessage = block
		}
	}
	return inject.BuildWorkerPromptContext(out), nil
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

var _ PlaybookMatcherInterface = (*prompts.PlaybookMatcher)(nil)

var _ assembly.WorkerContextBuilder = (*CompositeWorkerContext)(nil)
