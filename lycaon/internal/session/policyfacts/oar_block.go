package policyfacts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/pkg/api"
)

// tryOARBlock returns policy refusals separately from evaluation errors.
func (m *Service) Block(ctx context.Context, anchor string, sess *api.Session, tool string, args map[string]any, observe func(gc *oar.GuardContext) error) (*guidance.Refusal, bool, error) {
	if m == nil || m.Pipeline == nil || !m.Pipeline.AnchorEnforced(anchor) {
		return nil, false, nil
	}
	gc := oar.NewGuardContext()
	m.FillSessionFacts(ctx, gc, sess, tool, args)
	if m.MCP != nil {
		oar.ObserveMCPStructuralPre(gc, tool, m.MCP)
	}
	if observe != nil {
		if err := observe(gc); err != nil {
			return RejectFromGuardErr(ctx, err), true, nil
		}
	}
	return m.renderBlock(ctx, anchor, gc)
}

// RejectFromGuardErr converts a guard observation error into a refusal.
func RejectFromGuardErr(ctx context.Context, err error) *guidance.Refusal {
	if err == nil {
		return nil
	}
	var nudge *guidance.ErrGroundingNudge
	if errors.As(err, &nudge) && nudge != nil {
		body, renderErr := guidance.RenderPolicyCopy(ctx, nudge.Code, string(oar.EffectBlock), nudge.Copy)
		if renderErr != nil {
			body = nudge.Code
		}
		return guidance.NewRefusal(nudge.Code, body).WithPolicyCopy(nudge.Copy).WithDetails(nudge.Data, nil).WithCause(err)
	}
	return guidance.NewRefusal("", err.Error())
}

// CloseoutBlock runs coordinator.closeout_check after closeout citation facts.
func (m *Service) CloseoutBlock(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
	if m == nil || m.Pipeline == nil || !m.Pipeline.AnchorEnforced(oar.AnchorCoordinatorCloseoutCheck) {
		return nil, fmt.Errorf("closeout citation OAR requires enforced coordinator.closeout_check")
	}
	if gc == nil {
		gc = oar.NewGuardContext()
	}
	m.FillSessionFacts(ctx, gc, sess, "", nil)
	res, err := m.Pipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorCloseoutCheck, gc)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Enforced {
		return nil, fmt.Errorf("closeout citation OAR EvaluateBlock not enforced")
	}
	if res.Decision == nil {
		return nil, nil
	}
	d := res.Decision
	if d.Effect != oar.EffectBlock {
		return nil, nil
	}
	return d, nil
}

// FinishBlock evaluates the native provisional report checkpoint.
func (m *Service) FinishBlock(ctx context.Context, sess *api.Session, observe func(gc *oar.GuardContext) error) (*guidance.Refusal, bool) {
	if m == nil {
		return nil, false
	}
	gc := oar.NewGuardContext()
	m.FillSessionFacts(ctx, gc, sess, "", nil)
	if observe != nil {
		if err := observe(gc); err != nil {
			return RejectFromGuardErr(ctx, err), true
		}
	}
	if m.Pipeline == nil {
		return nil, false
	}
	anchor := oar.AnchorCoordinatorCloseoutCheck
	if sess != nil && sess.IsWorkerChild() {
		anchor = oar.AnchorWorkerReportCheck
	}
	if !m.Pipeline.AnchorEnforced(anchor) {
		return nil, false
	}
	reject, blocked, err := m.renderBlock(ctx, anchor, gc)
	if err != nil {
		return guidance.NewRefusal("", err.Error()), true
	}
	return reject, blocked
}

// renderOARBlock evaluates an anchor and renders its Decision.
func (m *Service) renderBlock(ctx context.Context, anchor string, gc *oar.GuardContext) (*guidance.Refusal, bool, error) {
	res, err := m.Pipeline.EvaluateBlock(ctx, anchor, gc)
	if err != nil {
		return nil, false, err
	}
	return m.feedback.RenderResult(ctx, anchor, res)
}

// FormatDoomLoopReject evaluates tool.rejected for DOOM_LOOP_REPEAT.
func (m *Service) FormatDoomLoopReject(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
	if m == nil {
		return nil, nil
	}
	var sess *api.Session
	if m.store != nil && sessionID != "" {
		if s, err := m.store.Get(ctx, sessionID); err == nil {
			sess = s
		}
	}
	if sess == nil {
		sess = &api.Session{ID: sessionID}
	}
	if m.Pipeline == nil || !m.Pipeline.AnchorEnforced(oar.AnchorToolRejected) {
		return nil, nil
	}
	reject, blocked, err := m.Block(ctx, oar.AnchorToolRejected, sess, tool, args, func(gc *oar.GuardContext) error {
		m.ObserveDeferredUnactivated(gc, sess.ID)
		loopguard.ObserveDoomLoopRepeat(gc, count, tool, repeatedCode, args)
		return nil
	})
	if err != nil {
		return guidance.NewRefusal("", err.Error()), nil
	}
	if blocked {
		return reject, nil
	}
	return nil, nil
}

// EscalateRepeatedCode preserves the original rejection when no escalation matches.
func (m *Service) EscalateRepeatedCode(ctx context.Context, sessionID, tool string, original *guidance.Refusal, total int) *guidance.Refusal {
	if m == nil || total <= 0 || original == nil || original.Code() == "" {
		return nil
	}
	var sess *api.Session
	if m.store != nil && sessionID != "" {
		if s, err := m.store.Get(ctx, sessionID); err == nil {
			sess = s
		}
	}
	if sess == nil {
		sess = &api.Session{ID: sessionID}
	}
	code := original.Code()
	instead := strings.TrimSpace(original.Copy["instead"])
	reject, blocked, err := m.Block(ctx, oar.AnchorToolRejected, sess, tool, nil, func(gc *oar.GuardContext) error {
		loopguard.ObserveDoomLoopCodeRepeat(gc, total, tool, code, instead)
		return nil
	})
	if err != nil {
		return guidance.NewRefusal("", err.Error())
	}
	if !blocked {
		return nil
	}
	return reject
}

// CodeRejectResponses counts rejected responses up to the escalation threshold.
func (m *Service) CodeRejectResponses(sessionID, tool, code string) int {
	if m == nil || m.doomLoop == nil {
		return 0
	}
	return m.doomLoop.CodeRejectResponses(sessionID, tool, code)
}

// AfterTool returns rendered guidance and structured result facts.
func (m *Service) AfterTool(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, doomCompletionCountAfter int, raised guidance.ToolResultFacts) (delivered string, facts guidance.ToolResultFacts) {
	defer raised.IndexWatch.Release()
	defer func() {
		if m != nil && sess != nil && raised.Succeeded() && facts.Succeeded() {
			var candidateFacts guidance.ToolResultFacts
			delivered, candidateFacts = m.evaluateCredentialCandidates(ctx, sess, tool, args, delivered)
			facts = facts.Merge(candidateFacts)
		}
	}()
	if m == nil || sess == nil {
		return output, facts
	}
	if m.Pipeline == nil || !m.Pipeline.AnchorEnforced(oar.AnchorToolPost) {
		return output, facts
	}
	gc := oar.NewGuardContext()
	m.FillSessionFacts(ctx, gc, sess, tool, args)
	gc.SetContentSegments([]oar.ContentSegment{{Content: output, Role: "tool", Origin: "tool", Authority: "none", TrustTier: "untrusted", Source: tool}})
	if strings.HasPrefix(tool, "mcp_") && m.MCP != nil {
		oar.ObserveMCPStructuralPost(gc, tool, m.MCP, raised.Succeeded(), raised.ProviderErrorCode, output)
	}
	ObserveConfine(gc, raised.Confine)
	RegisterWorktreeFacts(ctx, gc, raised.IndexWatch)
	ObserveEditorConfigMismatch(gc, raised)
	ObserveHTTPRequestWebPage(gc, raised)
	ObserveSourceParsingFeedback(gc, raised)
	m.ObserveDeferredUnactivated(gc, sess.ID)
	loopguard.ObserveDoomLoopWarn(gc, doomCompletionCountAfter, loopguard.DoomLoopMaxAttempts, tool, args)
	if run, pattern := m.fruitlessSearchRun(sess, tool, args); run > 0 {
		loopguard.ObserveFruitlessSearch(gc, run, tool, pattern)
	}
	if raised.Succeeded() {
		m.ObserveMintedCredential(ctx, sess, tool, args, output)
	}
	res, err := m.Pipeline.EvaluateBlock(ctx, oar.AnchorToolPost, gc)
	if err != nil {
		facts.ContentReplaced = true
		return "", facts.WithOutcome(api.ToolResultOutcomeError)
	}
	return m.feedback.DeliverPostToolResult(ctx, oar.AnchorToolPost, output, res)
}

// fruitlessSearchRun reads question-specific counts without recording an attempt.
func (m *Service) fruitlessSearchRun(sess *api.Session, tool string, args map[string]any) (int, string) {
	if m == nil || sess == nil || m.doomLoop == nil {
		return 0, ""
	}
	pattern, _ := args["pattern"].(string)
	return m.doomLoop.FruitlessSearchRun(sess.ID, tool, args), pattern
}

// ContentBlock returns the content decision and any rendered refusal.
func (m *Service) ContentBlock(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool, error) {
	if m == nil || m.Pipeline == nil || !m.Pipeline.AnchorEnforced(anchor) {
		return nil, false, "", false, nil
	}
	gc := oar.NewGuardContext()
	m.FillSessionFacts(ctx, gc, sess, tool, args)
	gc.SetContentSegments(segments)
	res, err := m.Pipeline.EvaluateBlock(ctx, anchor, gc)
	if err != nil {
		return nil, false, "", false, err
	}
	if res == nil || !res.Enforced {
		return nil, false, "", false, nil
	}
	content := gc.Content.Content
	if res.ContentSet {
		content = res.Content
	}
	if res.Decision == nil {
		if res.ContentSet && res.Content != gc.Content.Content {
			return nil, false, content, true, nil
		}
		return nil, false, "", false, nil
	}
	if res.Decision.Effect == oar.EffectTransform {
		return nil, false, content, true, nil
	}
	if res.Decision.Effect == oar.EffectWarn || res.Decision.Effect == oar.EffectNudge {
		return nil, false, "", false, nil
	}
	reject, blocked, rerr := m.feedback.RenderResult(ctx, anchor, res)
	if rerr != nil {
		return nil, false, "", false, rerr
	}
	return reject, blocked, "", false, nil
}
