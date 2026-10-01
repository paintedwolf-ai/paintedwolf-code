package toolhost

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const approvalRationaleMaxTokens = 120

// mockAIRationaleStub is returned under LYCAON_LLM_MOCK so tests can assert the
// fire-and-patch path without a provider.
const mockAIRationaleStub = "Advances the current task toward the stated goal."

// MessageLoader loads transcript rows for a session.
type MessageLoader interface {
	GetMessages(ctx context.Context, sessionID string) ([]api.Message, error)
}

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

// ApprovalRationaleDeps wires read-side deps for AI rationale attach.
type ApprovalRationaleDeps struct {
	Messages    MessageLoader
	Workers     WorkerTaskLoader
	Progress    ProgressContentLoader
	Root        RootSessionResolver
	Summarizer  compaction.Summarizer
	Checkpoints hitl.CheckpointManager
	// EnabledFn reports whether the global AI-rationale setting is on. Nil means
	// enabled.
	EnabledFn func() bool
}

type approvalRationaleAttacher struct {
	deps ApprovalRationaleDeps
}

// NewApprovalRationaleAttacher builds a tools.AIRationaleAttacher that fire-and-patches
// ai_rationale onto pending tool_approval checkpoints.
func NewApprovalRationaleAttacher(deps ApprovalRationaleDeps) tools.AIRationaleAttacher {
	if deps.Checkpoints == nil || deps.Messages == nil || deps.Summarizer == nil {
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

func (a *approvalRationaleAttacher) AttachAsync(ctx context.Context, req tools.AIRationaleAttachRequest) {
	if a == nil || strings.TrimSpace(req.CheckpointID) == "" {
		return
	}
	if !a.Enabled() {
		return
	}
	// The provider bounds the call independently of the turn wait.
	go a.run(context.WithoutCancel(ctx), req)
}

// The provider controls the timeout, including model loading.
func (a *approvalRationaleAttacher) run(parent context.Context, req tools.AIRationaleAttachRequest) {
	ctx := curationctx.WithSession(parent, curationctx.Session{
		SessionID:       req.ToolContext.SessionID,
		ProjectID:       req.ToolContext.ProjectID,
		Agent:           req.ToolContext.Agent,
		ParentSessionID: req.ToolContext.ParentSessionID,
		ToolCallID:      req.ToolContext.ToolCallID,
		ProjectDir:      req.ToolContext.ActiveRootPath(),
	})

	in := a.loadInputs(ctx, req)

	var text string
	if llm.MockOnlyFromEnv() {
		text = mockAIRationaleStub
	} else if sys, sysErr := approvals.FormatRationaleSystemPrompt(ctx); sysErr == nil {
		if userPrompt, userErr := approvals.FormatRationaleUserPrompt(ctx, in); userErr == nil && userPrompt != "" {
			if out, err := a.deps.Summarizer.Summarize(ctx, sys, userPrompt, approvalRationaleMaxTokens); err == nil {
				text = approvals.ClipRationale(out)
			}
		}
	}

	if text == "" {
		// Fail-soft: no rationale produced — collapse the reserved slot.
		_ = a.deps.Checkpoints.ClearPendingToolApprovalAIRationale(ctx, req.CheckpointID)
		return
	}
	text = strings.TrimSpace(observability.RedactCaptureText(text))
	if text == "" {
		_ = a.deps.Checkpoints.ClearPendingToolApprovalAIRationale(ctx, req.CheckpointID)
		return
	}
	_ = a.deps.Checkpoints.PatchPendingToolApprovalAIRationale(ctx, req.CheckpointID, text)
}

func (a *approvalRationaleAttacher) loadInputs(ctx context.Context, req tools.AIRationaleAttachRequest) approvals.RationaleInputs {
	tc := req.ToolContext
	args, _ := observability.RedactCaptureValue(req.Args).(map[string]any)
	in := approvals.RationaleInputs{
		Tool:    req.Tool,
		Args:    args,
		Files:   append([]string(nil), req.Files...),
		Project: tc.ActiveRootPath(),
	}
	if req.Explanation != nil {
		in.ExplanationWhat = req.Explanation.What
		in.ExplanationWho = req.Explanation.Who
		in.ExplanationIfWrong = req.Explanation.IfWrong
	}

	if a.deps.Workers != nil && strings.TrimSpace(tc.WorkerJobID) != "" {
		if task, ok := a.deps.Workers.Get(tc.WorkerJobID); ok && task != nil {
			in.WorkerBrief = strings.TrimSpace(task.Brief)
		}
	}

	sessionID := strings.TrimSpace(tc.SessionID)
	if sessionID != "" && a.deps.Messages != nil {
		msgs, err := a.deps.Messages.GetMessages(ctx, sessionID)
		if err == nil {
			msgs = rationaleMessagesThroughAction(msgs, tc.ToolCallID)
			boundary := api.UserIntentBoundary(msgs)
			if boundary > 0 && boundary <= len(msgs) {
				userMsg := msgs[boundary-1]
				in.UserIntent = strings.TrimSpace(userMsg.Content)
			}
			in.AssistantProse = assistantProseForToolCall(msgs, tc.ToolCallID)
			in.RecentResults = recentRationaleResults(msgs, boundary, tc.ToolCallID)
		}
	}

	if a.deps.Progress != nil && a.deps.Root != nil && sessionID != "" {
		rootID := a.deps.Root.RootSessionID(ctx, sessionID)
		content := a.deps.Progress.Get(ctx, rootID)
		in.ProgressSteps = progress.DeriveProgress(content, progress.DefaultProgressCap).Items
	}
	return redactRationaleInputs(in)
}

func redactRationaleInputs(in approvals.RationaleInputs) approvals.RationaleInputs {
	in.Tool = observability.RedactCaptureText(in.Tool)
	in.Project = observability.RedactCaptureText(in.Project)
	in.ExplanationWhat = observability.RedactCaptureText(in.ExplanationWhat)
	in.ExplanationWho = observability.RedactCaptureText(in.ExplanationWho)
	in.ExplanationIfWrong = observability.RedactCaptureText(in.ExplanationIfWrong)
	in.WorkerBrief = observability.RedactCaptureText(in.WorkerBrief)
	in.UserIntent = observability.RedactCaptureText(in.UserIntent)
	in.AssistantProse = observability.RedactCaptureText(in.AssistantProse)
	in.RecentResults = observability.RedactCaptureText(in.RecentResults)
	for i := range in.Files {
		in.Files[i] = observability.RedactCaptureText(in.Files[i])
	}
	for i := range in.ProgressSteps {
		in.ProgressSteps[i].Label = observability.RedactCaptureText(in.ProgressSteps[i].Label)
		in.ProgressSteps[i].State = observability.RedactCaptureText(in.ProgressSteps[i].State)
	}
	return in
}

func assistantProseForToolCall(msgs []api.Message, toolCallID string) string {
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		return ""
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != api.MessageRoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			if strings.TrimSpace(tc.ID) == toolCallID {
				return strings.TrimSpace(m.Content)
			}
		}
	}
	return ""
}
