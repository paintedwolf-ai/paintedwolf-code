package delegation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/session/profiles"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompositeWorkerContext selects delegation or task-spawn context.
type CompositeWorkerContext struct {
	Delegation       *WorkerContextLoader
	Records          *workerrecords.Store
	Tools            LegToolLister
	AgentsFor        func(*api.Session) profiles.AgentProfileResolver
	MatcherFor       func(*api.Session) PlaybookMatcherInterface
	WorkflowIDForRun func(ctx context.Context, runID string) string
	// ReviewSubject names the result a reviewer's job reads and the worker
	// that delivered it; ok is false for any other job.
	ReviewSubject func(ctx context.Context, jobID string) (worker, handle string, ok bool, err error)
}

func (c *CompositeWorkerContext) BuildWorkerPromptContext(ctx context.Context, childID string, sess *api.Session) (inject.WorkerLegContext, error) {
	if c != nil && c.Delegation != nil {
		leg, err := c.Delegation.BuildWorkerPromptContext(ctx, childID, sess)
		if err == nil {
			return leg, nil
		}
		if !errors.Is(err, errWorkerNotDelegated) {
			return inject.WorkerLegContext{}, err
		}
	}
	return buildTaskSpawnWorkerContext(ctx, c, sess)
}

func buildTaskSpawnWorkerContext(ctx context.Context, c *CompositeWorkerContext, sess *api.Session) (inject.WorkerLegContext, error) {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) == "" {
		return inject.WorkerLegContext{}, fmt.Errorf("not a worker child session")
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
	var scans scan.WorkerScanLister
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
	if c != nil && c.Delegation != nil {
		scans = c.Delegation.Scans
		if c.Delegation.Tasks != nil {
			// The task tool stamps the run on the job; the session carries none.
			if task, ok := c.Delegation.Tasks.GetLatestByChildSessionID(ctx, sess.ID); ok && task != nil {
				if runID == "" {
					runID = task.WorkflowRunID
				}
				if out.PhaseID == "" {
					out.PhaseID = task.WorkflowPhase
				}
				if len(legFiles) == 0 {
					legFiles = append([]string(nil), task.Files...)
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
		if runID != "" {
			if c.WorkflowIDForRun != nil {
				out.WorkflowID = strings.TrimSpace(c.WorkflowIDForRun(ctx, runID))
			}
			if c.Delegation.Topology != nil && out.WorkflowID != "" {
				if tp := strings.TrimSpace(c.Delegation.Topology(out.WorkflowID)); tp != "" {
					out.TopologyPattern = tp
				}
			}
			if out.PhaseID == "" && c.Delegation.Runs != nil {
				if phase, pErr := c.Delegation.Runs.PhaseForRun(ctx, runID); pErr == nil {
					out.PhaseID = strings.TrimSpace(phase)
				}
			}
		}
		out.LayoutTopLevel = layoutTopLevelForWorker(ctx, c.Delegation.Repo, sess.WorkspacePath)
	}
	if checklist, err := mergeTaskSpawnChecklist(matcher, agents, agentType, out.TopologyPattern, out.PhaseID); err != nil {
		return out, err
	} else if len(checklist) > 0 {
		out.Checklist = checklist
	}
	var err error
	out, err = attachWorkerScanDigest(ctx, out, sess.WorkspacePath, runID, scans)
	if err != nil {
		return out, err
	}
	if c != nil && c.Delegation != nil {
		block, err := c.Delegation.attachAgentsMDChain(ctx, sess, legFiles)
		if err != nil {
			return out, err
		}
		if strings.TrimSpace(block.Content) != "" {
			out.AgentsMDMessage = block
		}
	}
	return inject.BuildWorkerPromptContext(out), nil
}

var _ PlaybookMatcherInterface = (*prompts.PlaybookMatcher)(nil)

var _ assembly.WorkerContextBuilder = (*CompositeWorkerContext)(nil)
