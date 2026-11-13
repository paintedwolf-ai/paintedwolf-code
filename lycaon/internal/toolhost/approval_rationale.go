package toolhost

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolapproval"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApprovalProfileID names the approval card's purpose sentence among
// briefing profiles.
const ApprovalProfileID = "approval_rationale"

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

// CheckpointPins keeps a record for as long as its checkpoint exists.
type CheckpointPins interface {
	PinCheckpoint(ctx context.Context, projectID, checkpointID, recordID string) error
}

// ApprovalRationaleDeps wires the live evidence and the shared briefing
// engine for approval rationales.
type ApprovalRationaleDeps struct {
	Cause       briefing.CausalReader
	Workers     WorkerTaskLoader
	Progress    ProgressContentLoader
	Root        RootSessionResolver
	Engine      *briefing.Engine
	Router      *briefing.Router
	Pins        CheckpointPins
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
	if deps.Checkpoints == nil || deps.Cause == nil || deps.Engine == nil || deps.Router == nil {
		return nil
	}
	attacher := &approvalRationaleAttacher{deps: deps}
	deps.Router.Handle(ApprovalProfileID, attacher.settle)
	return attacher
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
	identity := req.ToolContext.Identity
	section, err := briefing.PurposeSection(ctx, a.evidence(ctx, req))
	if err != nil {
		_ = a.deps.Checkpoints.ClearPendingToolApprovalAIRationale(ctx, req.CheckpointID)
		return
	}
	snap, err := a.deps.Engine.Request(ctx, briefing.Request{
		ProjectID: identity.ProjectID, ProjectDir: req.ToolContext.ActiveRootPath(),
		ProfileID: ApprovalProfileID, Version: approvalProfileVersion, TargetKey: req.CheckpointID,
		TargetKind: "effect", TargetJSON: approvalTarget(req), ScopeFingerprint: identity.ToolCallID,
		Policy: briefing.ApprovalPolicy, Sections: []briefing.SectionPlan{section},
	})
	if err != nil {
		_ = a.deps.Checkpoints.ClearPendingToolApprovalAIRationale(ctx, req.CheckpointID)
		return
	}
	if snap.Status == briefing.StatusComplete {
		a.settle(ctx, identity.ProjectID, snap)
	}
}

const approvalProfileVersion = "1"

func approvalTarget(req toolapproval.AIRationaleAttachRequest) string {
	raw, err := json.Marshal(struct {
		CheckpointID string `json:"checkpoint_id"`
		ToolCallID   string `json:"tool_call_id"`
		Tool         string `json:"tool"`
	}{req.CheckpointID, req.ToolContext.Identity.ToolCallID, req.Tool})
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// settle shows a settled sentence on its still-pending card and pins the
// record only when the card took it.
func (a *approvalRationaleAttacher) settle(ctx context.Context, projectID string, snap briefing.Snapshot) {
	text := ""
	if snap.Status == briefing.StatusComplete && len(snap.Sections) == 1 && snap.Sections[0].Result != nil {
		text = snap.Sections[0].Result.Text
	}
	if text == "" {
		_ = a.deps.Checkpoints.ClearPendingToolApprovalAIRationale(ctx, snap.TargetKey)
		return
	}
	applied, err := a.deps.Checkpoints.PatchPendingToolApprovalAIRationale(ctx, snap.TargetKey, text)
	if err != nil || !applied || a.deps.Pins == nil {
		return
	}
	_ = a.deps.Pins.PinCheckpoint(ctx, projectID, snap.TargetKey, snap.RecordID)
}

// evidence reads what the approval card has always used, through the bounded
// causal read rather than the whole transcript.
func (a *approvalRationaleAttacher) evidence(ctx context.Context, req toolapproval.AIRationaleAttachRequest) briefing.ActionEvidence {
	identity := req.ToolContext.Identity
	ev := briefing.ActionEvidence{Action: briefing.ActionFacts{
		Tool: req.Tool, CallRef: "tool_call:" + identity.ToolCallID, Args: req.Args,
		Files: append([]string(nil), req.Files...), Project: req.ToolContext.ActiveRootPath(),
	}}
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
				ev.Plan.Steps = append(ev.Plan.Steps, briefing.PlanStep{Label: step.Label, State: step.State})
			}
		}
	}
	return ev
}
