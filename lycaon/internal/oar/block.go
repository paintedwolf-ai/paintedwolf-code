package oar

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

// Catalog anchors used as the Emit/Binding block plane.
const (
	AnchorToolPreInvoke            = "tool.pre_invoke"
	AnchorCoordinatorPreInvoke     = "coordinator.pre_invoke"
	AnchorSessionPreInvoke         = "session.pre_invoke"
	AnchorToolHandler              = "tool.handler"
	AnchorToolRejected             = "tool.rejected"
	AnchorCoordinatorPostTurn      = "coordinator.post_turn"
	AnchorCoordinatorCloseoutCheck = "coordinator.closeout_check"
	AnchorWorkerFinalize           = "worker.finalize"
	AnchorWorkerReportCheck        = "worker.report_check"
	AnchorToolPost                 = "tool.post_invoke"
	AnchorCredentialAssignment     = "credential.assignment"
	// Content-safety anchors implementing the three core model-IO anchors.
	AnchorContentInput      = "content.input"
	AnchorContentOutput     = "content.output"
	AnchorContentToolResult = "content.tool_result"
)

// PutRejectData stores Decision.Data for code without adding to arg_validation_errors.
func (gc *GuardContext) PutRejectData(code string, data map[string]any) {
	if gc == nil || code == "" {
		return
	}
	if gc.RejectData == nil {
		gc.RejectData = map[string]map[string]any{}
	}
	if data == nil {
		data = map[string]any{}
	}
	gc.RejectData[code] = data
	if gc.ObservationData == nil {
		gc.ObservationData = map[string]any{}
	}
	for key, value := range data {
		gc.ObservationData[key] = value
	}
}

// EnableAnchor turns on authoritative EvaluateBlock for a catalog Anchor.
func (p *GuardPipeline) EnableAnchor(anchor string) {
	if p == nil {
		return
	}
	if p.enforcedAnchors == nil {
		p.enforcedAnchors = map[string]bool{}
	}
	p.enforcedAnchors[strings.TrimSpace(anchor)] = true
}

// AnchorEnforced reports whether EvaluateBlock is authoritative for anchor.
func (p *GuardPipeline) AnchorEnforced(anchor string) bool {
	if p == nil || p.enforcedAnchors == nil {
		return false
	}
	return p.enforcedAnchors[strings.TrimSpace(anchor)]
}

// EvaluateBlock runs OAR rules bound to a catalog Anchor and is authoritative
// when EnableAnchor was called for that id.
func (p *GuardPipeline) EvaluateBlock(ctx context.Context, anchor string, gc *GuardContext) (*PipelineResult, error) {
	res := &PipelineResult{}
	if p == nil {
		return res, nil
	}
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return res, fmt.Errorf("oar: empty block anchor")
	}
	res.Enforced = p.AnchorEnforced(anchor)
	if !res.Enforced {
		return res, nil
	}
	if gc == nil {
		gc = NewGuardContext()
	}
	unlock := p.counters.beginOccurrence(gc.Session.SessionID)
	defer unlock()
	rules := p.effectiveRules(ctx, gc.Session.SessionID)
	if rules == nil {
		return res, nil
	}
	p.registerFactProviders(gc)
	p.observeActivity(gc)
	gc.DeriveToolClassFacts()
	holder := newEvalHolder(gc, rules, p.counters)
	publishOccurrenceAnchor(gc, anchor, p.capability())

	matched, selectionErrors := p.selectRulesForAnchor(rules, anchor, gc)
	if err := p.evaluateMatched(ctx, holder, gc, matched, selectionErrors, StageFromAnchor(anchor), res); err != nil {
		return res, err
	}
	if err := p.deliverAdvisories(ctx, anchor, gc, res); err != nil {
		return res, err
	}
	if gc.Anchor == CoreAnchorToolPreInvoke && gc.Invocation.Tool != "" && (res.Decision == nil || res.Decision.Effect != EffectBlock) {
		p.AdmitTool(gc.Session.SessionID, gc.Invocation.Tool)
	}
	return res, nil
}

func rejectDataFor(gc *GuardContext, code string) map[string]any {
	if gc == nil {
		return nil
	}
	var d map[string]any
	if gc.RejectData != nil {
		d = gc.RejectData[code]
	}
	out := maps.Clone(gc.FeedbackData)
	if out == nil {
		out = map[string]any{}
	}
	maps.Copy(out, d)
	if _, ok := out["tool"]; !ok && gc.Invocation.Tool != "" {
		out["tool"] = gc.Invocation.Tool
	}
	if gc.Session.Profile != "" {
		if _, ok := out["profile"]; !ok {
			out["profile"] = gc.Session.Profile
		}
	}
	out["worker_leg"] = gc.Session.WorkerLeg
	return out
}

func (p *GuardPipeline) selectRulesForAnchor(rules *RuleSet, anchor string, gc *GuardContext) ([]*Rule, map[*Rule]error) {
	if rules == nil {
		return nil, nil
	}
	var out []*Rule
	errs := map[*Rule]error{}
	for _, r := range rules.OnAnchor(anchor) {
		if enforcementOff(r) {
			continue
		}
		if err := AssembleFacts(gc, r.Selector.Clauses()); err != nil {
			out = append(out, r)
			errs[r] = err
			continue
		}
		matches, err := ruleSelectorMatches(r, gc)
		if err != nil {
			out = append(out, r)
			errs[r] = err
			continue
		}
		if !matches {
			continue
		}
		out = append(out, r)
	}
	return out, errs
}
