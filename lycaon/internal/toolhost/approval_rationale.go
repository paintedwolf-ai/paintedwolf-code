package toolhost

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolapproval"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalProfileID and ApprovalProfileVersion identify the approval card's
// purpose sentence among briefing profiles.
const (
	ApprovalProfileID      = "approval_rationale"
	ApprovalProfileVersion = "1"
)

// WorkerTaskLoader loads a worker assignment by job id.
type WorkerTaskLoader interface {
	Get(jobID string) (*api.WorkerTask, bool)
}

// ProgressContentLoader returns the root-session checklist markdown.
type ProgressContentLoader interface {
	Get(ctx context.Context, sessionID string) string
}

// RootSessionResolver walks parent_session_id to the coordinator root.
type RootSessionResolver interface {
	RootSessionID(ctx context.Context, sessionID string) string
}

// ApprovalRationaleDeps wires the live evidence and generation for approval
// rationales.
type ApprovalRationaleDeps struct {
	Cause       briefing.CausalReader
	Workers     WorkerTaskLoader
	Progress    ProgressContentLoader
	Root        RootSessionResolver
	Generator   briefing.Generator
	Checkpoints hitl.CheckpointManager
	// EnabledFn reports whether the global AI-rationale setting is on. Nil means
	// enabled.
	EnabledFn func() bool
}

type approvalRationaleAttacher struct {
	deps ApprovalRationaleDeps
}

// NewApprovalRationaleAttacher returns nil when no model can explain an
// approval, so cards never reserve a rationale slot that cannot fill.
func NewApprovalRationaleAttacher(deps ApprovalRationaleDeps) toolapproval.AIRationaleAttacher {
	if deps.Checkpoints == nil || deps.Cause == nil || deps.Generator == nil {
		return nil
	}
	return &approvalRationaleAttacher{deps: deps}
}

// Enabled reports whether the global AI-rationale setting is on.
func (a *approvalRationaleAttacher) Enabled() bool {
	if a == nil {
		return false
	}
	if a.deps.EnabledFn == nil {
		return true
	}
	return a.deps.EnabledFn()
}

func (a *approvalRationaleAttacher) AttachAsync(ctx context.Context, req toolapproval.AIRationaleAttachRequest) {
	if a == nil || strings.TrimSpace(req.CheckpointID) == "" || !a.Enabled() {
		return
	}
	// The provider bounds the call independently of the turn wait.
	go a.run(context.WithoutCancel(ctx), req)
}

func (a *approvalRationaleAttacher) run(parent context.Context, req toolapproval.AIRationaleAttachRequest) {
	ctx := curationctx.WithSession(parent, curationctx.Session{
		SessionID:       req.ToolContext.Identity.SessionID,
		ProjectID:       req.ToolContext.Identity.ProjectID,
		Agent:           req.ToolContext.Identity.Agent,
		ParentSessionID: req.ToolContext.Identity.ParentSessionID,
		ToolCallID:      req.ToolContext.Identity.ToolCallID,
		ProjectDir:      req.ToolContext.ActiveRootPath(),
	})
	text := ""
	if section, err := briefing.PurposeSection(ctx, ApprovalProfileID, ApprovalProfileVersion, a.evidence(ctx, req)); err == nil {
		raw, _, _, genErr := a.deps.Generator.Generate(ctx, briefing.Invocation{Purpose: ApprovalProfileID, Requested: true, Prompt: section.Prompt})
		if genErr == nil {
			if parsed, parseErr := section.Manifest.Contract.Parse(raw); parseErr == nil {
				text = strings.TrimSpace(observability.RedactCaptureText(parsed))
			}
		}
	}
	if text == "" {
		_ = a.deps.Checkpoints.ClearPendingToolApprovalAIRationale(ctx, req.CheckpointID)
		return
	}
	_ = a.deps.Checkpoints.PatchPendingToolApprovalAIRationale(ctx, req.CheckpointID, text)
}

// evidence reads what the approval card has always used, through the bounded
// causal read rather than the whole transcript.
func (a *approvalRationaleAttacher) evidence(ctx context.Context, req toolapproval.AIRationaleAttachRequest) briefing.ActionEvidence {
	identity := req.ToolContext.Identity
	ev := briefing.ActionEvidence{
		Tool: req.Tool, CallRef: "tool_call:" + identity.ToolCallID, Args: req.Args,
		Files: append([]string(nil), req.Files...), Project: req.ToolContext.ActiveRootPath(),
	}
	if req.Explanation != nil {
		ev.HostFacts.Text = strings.TrimSpace(strings.Join([]string{req.Explanation.What, req.Explanation.Who, req.Explanation.IfWrong}, "\n"))
	}
	if a.deps.Workers != nil && strings.TrimSpace(identity.WorkerJobID) != "" {
		if task, ok := a.deps.Workers.Get(identity.WorkerJobID); ok && task != nil {
			ev.WorkerBrief = briefing.Excerpt{Text: task.Brief, Ref: "worker_job:" + identity.WorkerJobID}
		}
	}
	if strings.TrimSpace(identity.SessionID) != "" {
		read, err := a.deps.Cause.ReadActionCause(ctx, briefing.ActionRef{SessionID: identity.SessionID, WorkerJobID: identity.WorkerJobID, ToolCallID: identity.ToolCallID})
		if err == nil {
			briefing.ApplyCause(&ev, read)
		}
		if a.deps.Progress != nil && a.deps.Root != nil {
			content := a.deps.Progress.Get(ctx, a.deps.Root.RootSessionID(ctx, identity.SessionID))
			for _, step := range progress.DeriveProgress(content, progress.DefaultProgressCap).Items {
				ev.Plan = append(ev.Plan, briefing.PlanStep{Label: step.Label, State: step.State})
			}
		}
	}
	return ev
}
