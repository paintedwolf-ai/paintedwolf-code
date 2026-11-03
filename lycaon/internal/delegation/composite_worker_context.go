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
	"github.com/lycaon/lycaon/pkg/api"
)

// CompositeWorkerContext selects delegation or task-spawn context.
type CompositeWorkerContext struct {
	Delegation *WorkerContextLoader
	Tools      LegToolLister
	AgentsFor  func(*api.Session) profiles.AgentProfileResolver
	MatcherFor func(*api.Session) PlaybookMatcherInterface
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
	if checklist, err := mergeTaskSpawnChecklist(matcher, agents, agentType, out.TopologyPattern); err != nil {
		return out, err
	} else if len(checklist) > 0 {
		out.Checklist = checklist
	}
	var scans scan.WorkerScanLister
	runID := ""
	if c != nil && c.Delegation != nil {
		scans = c.Delegation.Scans
		if c.Delegation.Tasks != nil {
			// The task tool stamps the run on the job; the session carries none.
			if task, ok := c.Delegation.Tasks.GetLatestByChildSessionID(ctx, sess.ID); ok && task != nil {
				runID = task.WorkflowRunID
			}
		}
	}
	var err error
	out, err = attachWorkerScanDigest(ctx, out, sess.WorkspacePath, runID, scans)
	if err != nil {
		return out, err
	}
	if c != nil && c.Delegation != nil {
		block, err := c.Delegation.attachAgentsMDChain(ctx, sess, nil)
		if err != nil {
			return out, err
		}
		out.AgentsMDMessage = block
	}
	return inject.BuildWorkerPromptContext(out), nil
}

var _ PlaybookMatcherInterface = (*prompts.PlaybookMatcher)(nil)

var _ assembly.WorkerContextBuilder = (*CompositeWorkerContext)(nil)
