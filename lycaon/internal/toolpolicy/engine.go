package toolpolicy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// Engine answers whether a tool may appear in the LLM schema and whether it may run.
type Engine interface {
	ListForPrompt(ctx context.Context, sess *api.Session, profileID string) []tools.ToolMeta
	EvaluateInvoke(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error
}

// RuleEvaluator evaluates scaffold rules before tool execution.
type RuleEvaluator interface {
	Evaluate(ctx context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error)
}

// PreInvokeGuard runs coordinator/profile guards before posture rules (optional).
type PreInvokeGuard func(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error

// EngineDeps wires prompt and invocation policy.
type EngineDeps struct {
	ToolInvoker      tools.ToolInvoker
	Rules            RuleEvaluator
	Workflows        *WorkflowDomains
	Postures         func(context.Context, *api.Session) (PostureRegistry, error)
	Limits           func(context.Context, *api.Session) settings.SessionLimits
	HasComposeDraft  func(context.Context, *api.Session) bool
	ToolAccess       func(context.Context, *api.Session) sandbox.ToolAccess
	RejectFormatter  *guidance.ToolRejectFormatter
	BlockPlane       *tools.BlockPlane
	PreInvoke        PreInvokeGuard
	ProjectRootCount func(context.Context, *api.Session) int
	OverlayRootPaths func(context.Context, *api.Session) []string
}

type engine struct {
	deps EngineDeps
}

// NewEngine constructs the prompt-path tool policy engine.
func NewEngine(deps EngineDeps) Engine {
	return &engine{deps: deps}
}

func (e *engine) ListForPrompt(ctx context.Context, sess *api.Session, profileID string) []tools.ToolMeta {
	if e == nil || e.deps.ToolInvoker == nil {
		return nil
	}
	filter := platform.ToolFilter{ProfileID: profileID}
	if e.deps.ToolAccess != nil {
		filter.ToolAccess = e.deps.ToolAccess(ctx, sess)
	}
	if roots, ok := coordinatorTurnFrameProjectRootCount(ctx); ok {
		filter.ProjectRootCount = roots
	} else if e.deps.ProjectRootCount != nil && sess != nil {
		filter.ProjectRootCount = e.deps.ProjectRootCount(ctx, sess)
	}
	base := tools.ListToolsForProfile(ctx, e.deps.ToolInvoker, filter)
	workerChild := sess.IsWorkerChild()
	out := make([]tools.ToolMeta, 0, len(base))
	for _, meta := range base {
		// A profile grants capability; only the session says which shape is running.
		if !toolcontract.AdmitsSession(meta.Name, workerChild) {
			continue
		}
		// Empty-args EvaluateInvoke hides tools that require arguments.
		if meta.Deferred || meta.IsMCP() {
			out = append(out, meta)
			continue
		}
		_, outcome, err := e.checkInvocation(ctx, sess, meta.Name, nil)
		if err != nil || (outcome != nil && !outcome.Allowed) {
			continue
		}
		out = append(out, meta)
	}
	return out
}

func (e *engine) EvaluateInvoke(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error {
	evalCtx, outcome, err := e.checkInvocation(ctx, sess, toolName, args)
	if err != nil {
		return err
	}
	if outcome != nil && !outcome.Allowed {
		return e.rejectRuleOutcome(ctx, sess, toolName, args, evalCtx, *outcome)
	}
	return nil
}

// Listing asks the same host requirements without inventing a tool.rejected occurrence.
func (e *engine) checkInvocation(ctx context.Context, sess *api.Session, tool string, args map[string]any) (rules.EvalContext, *rules.RuleOutcome, error) {
	if e == nil || sess == nil {
		return rules.EvalContext{}, nil, nil
	}
	if err := e.evaluatePreInvoke(ctx, sess, tool, args); err != nil {
		return rules.EvalContext{}, nil, err
	}
	if e.deps.Rules == nil {
		return rules.EvalContext{}, nil, nil
	}
	eval := BuildEvalContext(ctx, e.deps, sess, tool, args)
	outcome, err := e.deps.Rules.Evaluate(ctx, eval)
	if err != nil && errors.Is(err, settingsoverlay.ErrFormatInvalid) && isOverlayRemediationWrite(tool, args) {
		return eval, outcome, nil
	}
	return eval, outcome, err
}

// isOverlayRemediationWrite admits a content write to the overlay files an
// invalid format marker blocks, so the agent can repair them.
func isOverlayRemediationWrite(tool string, args map[string]any) bool {
	if !toolcontract.MutatesContent(tool) {
		return false
	}
	rawPath, _ := args["path"].(string)
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(rawPath)))
	if rel == "" {
		return false
	}
	formatRel := settingsoverlay.Rel(settingsoverlay.FormatFileName)
	ignoresRel := settingsoverlay.Rel(settingsoverlay.BasenameIgnores)
	return rel == formatRel || strings.HasSuffix(rel, "/"+formatRel) ||
		rel == ignoresRel || strings.HasSuffix(rel, "/"+ignoresRel)
}

// Host workflow requirements are intrinsic refusals; OAR owns their selected copy.
func (e *engine) rejectRuleOutcome(ctx context.Context, sess *api.Session, tool string, args map[string]any, eval rules.EvalContext, outcome rules.RuleOutcome) error {
	tr := &tools.ToolReject{Code: rejectCodeFor(outcome), FailureClass: api.FailureClassPolicyRejection, Data: map[string]any{
		"tool":                    tool,
		"reason":                  outcome.Message,
		"min_required":            outcome.MinRequired,
		"phase_required":          outcome.PhaseRequired,
		"phase_required_name":     outcome.PhaseRequiredName,
		"max_playbook":            outcome.MaxPlaybook,
		"next_action":             eval.PlanProgress.NextAction,
		"progress":                eval.PlanProgress.ProgressChecklist,
		"workflow_allowed_agents": append([]string{}, eval.AllowedAgents...),
	}}
	profile := sess.AgentType
	if profile == "" {
		profile = "coordinator"
	}
	err := e.deps.BlockPlane.RejectObservation(ctx, tool, profile, args, tr)
	if err == nil {
		err = tools.RenderReject(tr, nil)
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || strings.TrimSpace(outcome.PhaseRequired) == "" || e.deps.RejectFormatter == nil || refusal.Copy == nil {
		return err
	}
	block, renderErr := e.deps.RejectFormatter.FormatBlock(ctx, sess.ID, tool, rejectOutcomeView(outcome), eval.PlanProgress, refusal.Copy)
	if renderErr != nil {
		return err
	}
	refusal.Body = block
	return refusal
}

// rejectCodeFor is the code this refusal raises: the explicit reject code when
// the rule declared one, else the rule's own code.
func rejectCodeFor(outcome rules.RuleOutcome) string {
	if code := strings.TrimSpace(outcome.RejectCode); code != "" {
		return code
	}
	return strings.TrimSpace(outcome.Code)
}

func (e *engine) evaluatePreInvoke(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error {
	if e == nil || e.deps.PreInvoke == nil {
		return nil
	}
	return e.deps.PreInvoke(ctx, sess, toolName, args)
}

type rejectOutcomeView rules.RuleOutcome

func (v rejectOutcomeView) GetRejectCode() string    { return strings.TrimSpace(v.RejectCode) }
func (v rejectOutcomeView) GetPhaseRequired() string { return strings.TrimSpace(v.PhaseRequired) }
func (v rejectOutcomeView) GetPhaseRequiredName() string {
	return strings.TrimSpace(v.PhaseRequiredName)
}
func (v rejectOutcomeView) GetMinRequired() string { return strings.TrimSpace(v.MinRequired) }
func (v rejectOutcomeView) GetMaxPlaybook() string { return strings.TrimSpace(v.MaxPlaybook) }
