package session

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// tryOARBlock returns policy refusals separately from evaluation errors.
func (m *Manager) tryOARBlock(ctx context.Context, anchor string, sess *api.Session, tool string, args map[string]any, observe func(gc *oar.GuardContext) error) (*guidance.Refusal, bool, error) {
	if m == nil || m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(anchor) {
		return nil, false, nil
	}
	gc := oar.NewGuardContext()
	m.fillOARSessionFacts(ctx, gc, sess, tool, args)
	if m.mcpRuntime != nil {
		oar.ObserveMCPStructuralPre(gc, tool, m.mcpRuntime)
	}
	if observe != nil {
		if err := observe(gc); err != nil {
			return rejectFromGuardErr(ctx, err), true, nil
		}
	}
	return m.renderOARBlock(ctx, anchor, gc)
}

// rejectFromGuardErr converts a guard observation error into a refusal.
func rejectFromGuardErr(ctx context.Context, err error) *guidance.Refusal {
	if err == nil {
		return nil
	}
	var nudge *ErrGroundingNudge
	if errors.As(err, &nudge) && nudge != nil {
		body, renderErr := guidance.RenderPolicyCopy(ctx, nudge.Code, string(oar.EffectBlock), nudge.Copy)
		if renderErr != nil {
			body = nudge.Code
		}
		return guidance.NewRefusal(nudge.Code, body).WithPolicyCopy(nudge.Copy).WithDetails(nudge.Data, nil).WithCause(err)
	}
	return guidance.NewRefusal("", err.Error())
}

// evaluateOARCloseoutBlock runs coordinator.closeout_check after closeout citation facts.
func (m *Manager) evaluateOARCloseoutBlock(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error) {
	if m == nil || m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(oar.AnchorCoordinatorCloseoutCheck) {
		return nil, fmt.Errorf("closeout citation OAR requires enforced coordinator.closeout_check")
	}
	if gc == nil {
		gc = oar.NewGuardContext()
	}
	m.fillOARSessionFacts(ctx, gc, sess, "", nil)
	res, err := m.oarPipeline.EvaluateBlock(ctx, oar.AnchorCoordinatorCloseoutCheck, gc)
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

// tryOARFinishBlock evaluates the native provisional report checkpoint.
func (m *Manager) tryOARFinishBlock(ctx context.Context, sess *api.Session, observe func(gc *oar.GuardContext) error) (*guidance.Refusal, bool) {
	if m == nil {
		return nil, false
	}
	gc := oar.NewGuardContext()
	m.fillOARSessionFacts(ctx, gc, sess, "", nil)
	if observe != nil {
		if err := observe(gc); err != nil {
			return rejectFromGuardErr(ctx, err), true
		}
	}
	if m.oarPipeline == nil {
		return nil, false
	}
	anchor := oar.AnchorCoordinatorCloseoutCheck
	if sess != nil && sess.IsWorkerChild() {
		anchor = oar.AnchorWorkerReportCheck
	}
	if !m.oarPipeline.AnchorEnforced(anchor) {
		return nil, false
	}
	reject, blocked, err := m.renderOARBlock(ctx, anchor, gc)
	if err != nil {
		return guidance.NewRefusal("", err.Error()), true
	}
	return reject, blocked
}

func (m *Manager) fillOARSessionFacts(ctx context.Context, gc *oar.GuardContext, sess *api.Session, tool string, args map[string]any) {
	if gc == nil {
		return
	}
	if sess != nil {
		gc.Session.ProjectID = sess.ProjectID
		gc.Session.SessionID = sess.ID
		gc.Session.SessionPosture = string(sess.Posture)
		gc.Session.Principal = sess.OwnerPersonID
		gc.Session.WorkerLeg = sess.IsWorkerChild()
		if sess.ParentSessionID == "" {
			gc.Session.Profile = "coordinator"
		}
		gc.RegisterProvider("permission_profile", func(gc *oar.GuardContext) error {
			profile, err := m.promptToolProfile(ctx, sess)
			if err != nil {
				return err
			}
			gc.Session.PermissionProfile = profile
			return nil
		})
	}
	if caller, ok := people.Caller(ctx); ok {
		gc.Session.Principal = caller.ID
		gc.Session.PrincipalRoles = []string{string(caller.Role)}
	}
	tools.RegisterRecoveryFacts(ctx, gc)
	gc.ObserveToolCall(tool, args)
	gc.DeriveToolClassFacts()
}

// renderOARBlock evaluates an anchor and renders its Decision.
func (m *Manager) renderOARBlock(ctx context.Context, anchor string, gc *oar.GuardContext) (*guidance.Refusal, bool, error) {
	res, err := m.oarPipeline.EvaluateBlock(ctx, anchor, gc)
	if err != nil {
		return nil, false, err
	}
	return m.renderOARResult(ctx, anchor, res)
}

func (m *Manager) renderOARResult(ctx context.Context, anchor string, res *oar.PipelineResult) (*guidance.Refusal, bool, error) {
	if res == nil || !res.Enforced || res.Decision == nil {
		return nil, false, nil
	}
	blocks := res.Decision.Effect == oar.EffectBlock
	var fallback *guidance.Refusal
	if blocks {
		fallback = guidance.NewRefusal(res.Decision.Code, res.Decision.Code).WithPolicyCopy(res.Decision.Copy).
			WithDetails(policyFeedbackDetails(res.Decision.Rule, res.Decision.Data), guidanceNudgeSubject("", res.Decision.Data))
	}
	if m.oarRenderer == nil {
		return fallback, blocks, nil
	}
	rendered, rerr := m.oarRenderer.Render(ctx, oar.StageFromAnchor(anchor), res.Decision)
	if rerr != nil {
		return nil, false, rerr
	}
	if rd, ok := oar.FirstBlock(rendered); ok && rd.Text != "" {
		return guidance.NewRefusal(rd.Decision.Code, rd.Text).WithPolicyCopy(rd.Decision.Copy).WithDetails(policyFeedbackDetails(rd.Decision.Rule, rd.Decision.Data), guidanceNudgeSubject("", rd.Decision.Data)), true, nil
	}
	var bodies []string
	for _, rd := range rendered {
		if (rd.Channel == oar.ChannelBanner || rd.Channel == oar.ChannelGuidanceNudge) && rd.Text != "" {
			bodies = append(bodies, rd.Text)
		}
	}
	if len(bodies) > 0 {
		ref := guidance.NewRefusal("", strings.Join(bodies, "\n")).WithPolicyCopy(res.Decision.Copy)
		if !blocks {
			ref.Facts.Outcome = api.ToolResultOutcomeCompleted
		}
		for _, a := range res.Decision.Advisories {
			ref.Facts = ref.Facts.WithFeedback(a.Code, policyFeedbackDetails(a.Rule, a.Data), guidanceNudgeSubject("", a.Data))
		}
		return ref, blocks, nil
	}
	return fallback, blocks, nil
}

// formatDoomLoopReject evaluates tool.rejected for DOOM_LOOP_REPEAT.
func (m *Manager) formatDoomLoopReject(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
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
	if m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(oar.AnchorToolRejected) {
		return nil, nil
	}
	reject, blocked, err := m.tryOARBlock(ctx, oar.AnchorToolRejected, sess, tool, args, func(gc *oar.GuardContext) error {
		m.observeDeferredUnactivated(gc, sess.ID)
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

// escalateRepeatedCode preserves the original rejection when no escalation matches.
func (m *Manager) escalateRepeatedCode(ctx context.Context, sessionID, tool string, original *guidance.Refusal, total int) *guidance.Refusal {
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
	reject, blocked, err := m.tryOARBlock(ctx, oar.AnchorToolRejected, sess, tool, nil, func(gc *oar.GuardContext) error {
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
func (m *Manager) CodeRejectResponses(sessionID, tool, code string) int {
	if m == nil || m.doomLoop == nil {
		return 0
	}
	return m.doomLoop.CodeRejectResponses(sessionID, tool, code)
}

// appendPostToolGuidance returns rendered guidance and structured result facts.
func (m *Manager) appendPostToolGuidance(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, doomCompletionCountAfter int, raised guidance.ToolResultFacts) (delivered string, facts guidance.ToolResultFacts) {
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
	if m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(oar.AnchorToolPost) {
		return output, facts
	}
	gc := oar.NewGuardContext()
	m.fillOARSessionFacts(ctx, gc, sess, tool, args)
	gc.SetContentSegments([]oar.ContentSegment{{Content: output, Role: "tool", Origin: "tool", Authority: "none", TrustTier: "untrusted", Source: tool}})
	if strings.HasPrefix(tool, "mcp_") && m.mcpRuntime != nil {
		oar.ObserveMCPStructuralPost(gc, tool, m.mcpRuntime, raised.Succeeded(), raised.ProviderErrorCode, output)
	}
	ObserveConfine(gc, raised.Confine)
	registerWorktreeFacts(ctx, gc, raised.IndexWatch)
	ObserveEditorConfigMismatch(gc, raised)
	ObserveHTTPRequestWebPage(gc, raised)
	ObserveSourceParsingFeedback(gc, raised)
	m.observeDeferredUnactivated(gc, sess.ID)
	loopguard.ObserveDoomLoopWarn(gc, doomCompletionCountAfter, loopguard.DoomLoopMaxAttempts, tool, args)
	if run, pattern := m.fruitlessSearchRun(sess, tool, args); run > 0 {
		loopguard.ObserveFruitlessSearch(gc, run, tool, pattern)
	}
	if raised.Succeeded() {
		m.ObserveMintedCredential(ctx, sess, tool, args, output)
	}
	res, err := m.oarPipeline.EvaluateBlock(ctx, oar.AnchorToolPost, gc)
	if err != nil {
		facts.ContentReplaced = true
		return "", facts.WithOutcome(api.ToolResultOutcomeError)
	}
	return m.deliverPostToolResult(ctx, oar.AnchorToolPost, output, res)
}

func (m *Manager) deliverPostToolResult(ctx context.Context, anchor, output string, res *oar.PipelineResult) (string, guidance.ToolResultFacts) {
	facts := guidance.ToolResultFacts{}
	if res == nil || res.Decision == nil {
		return output, facts
	}
	decision := res.Decision
	if decision.Effect == oar.EffectNudge {
		return output, facts
	}
	if decision.Effect == oar.EffectTransform {
		facts.ContentReplaced = true
		return res.Content, facts.WithFeedback(decision.Code, policyFeedbackDetails(decision.Rule, decision.Data), guidanceNudgeSubject("", decision.Data))
	}
	banner, _, err := m.renderOARResult(ctx, anchor, res)
	if decision.Effect == oar.EffectBlock {
		facts.ContentReplaced = true
		facts = facts.WithFeedback(decision.Code, policyFeedbackDetails(decision.Rule, decision.Data), guidanceNudgeSubject("", decision.Data)).WithOutcome(api.ToolResultOutcomeRejected)
		if err != nil || banner == nil {
			return "", facts
		}
		return banner.Body, facts
	}
	if err != nil || banner == nil || strings.TrimSpace(banner.Body) == "" {
		return output, facts
	}
	for _, advisory := range decision.Advisories {
		facts = facts.WithFeedback(advisory.Code, policyFeedbackDetails(advisory.Rule, advisory.Data), guidanceNudgeSubject("", advisory.Data))
	}
	return output + "\n" + banner.Body, facts.WithFeedback(decision.Code, policyFeedbackDetails(decision.Rule, decision.Data), guidanceNudgeSubject("", decision.Data))
}

// fruitlessSearchRun reads question-specific counts without recording an attempt.
func (m *Manager) fruitlessSearchRun(sess *api.Session, tool string, args map[string]any) (int, string) {
	if m == nil || sess == nil || m.doomLoop == nil {
		return 0, ""
	}
	pattern, _ := args["pattern"].(string)
	return m.doomLoop.FruitlessSearchRun(sess.ID, tool, args), pattern
}

// tryOARContentBlock returns the content decision and any rendered refusal.
func (m *Manager) tryOARContentBlock(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool, error) {
	if m == nil || m.oarPipeline == nil || !m.oarPipeline.AnchorEnforced(anchor) {
		return nil, false, "", false, nil
	}
	gc := oar.NewGuardContext()
	m.fillOARSessionFacts(ctx, gc, sess, tool, args)
	gc.SetContentSegments(segments)
	res, err := m.oarPipeline.EvaluateBlock(ctx, anchor, gc)
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
	reject, blocked, rerr := m.renderOARResult(ctx, anchor, res)
	if rerr != nil {
		return nil, false, "", false, rerr
	}
	return reject, blocked, "", false, nil
}

func policyFeedbackDetails(rule string, data map[string]any) map[string]any {
	if rule == "" {
		return data
	}
	out := maps.Clone(data)
	if out == nil {
		out = map[string]any{}
	}
	out["policy_rule"] = rule
	return out
}
