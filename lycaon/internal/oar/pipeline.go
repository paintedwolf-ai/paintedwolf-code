package oar

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/oarcore"
)

// PipelineResult is the outcome of one GuardPipeline.Evaluate call.
type PipelineResult struct {
	// Decision is the resolved outcome ([OAR-EVAL-8]). Nil means none.
	// Nudge and warn list every same-effect fire on Advisories ([OAR-EVAL-20]).
	Decision *Decision
	Trace    AppliedRulesTrace
	// Enforced is false when EvaluateBlock runs for an Anchor that was not EnableAnchor'd.
	Enforced bool
	// Transforms is the accumulated mutation list when the resolved decision
	// is transform. A block discards every accumulated transform ([OAR-OPS-17]).
	Transforms        []*TransformSpec
	SkippedTransforms []oarcore.SkippedTransform
	// Content is the post-transform buffer when the occurrence carried content.
	Content    string
	ContentSet bool
	// AppliedOnFire is every on_fire action of a rule that fired and was
	// enforced, in evaluation order, including a rule whose effect lost
	// precedence ([OAR-EVAL-12]).
	AppliedOnFire []OnFireAction
	// PublishedEvents reports publish_event records accepted by the host stream.
	PublishedEvents []OnFireEvent
}

// RuleSetForFunc resolves the RuleSet for one evaluation. Nil or a nil return
// keeps the device RuleSet installed at pipeline construction.
type RuleSetForFunc func(ctx context.Context, sessionID string) *RuleSet

// GuardPipeline runs OAR rules at a lifecycle stage (or concrete anchor).
type GuardPipeline struct {
	rules           *RuleSet
	ruleSetFor      RuleSetForFunc
	counters        *CounterStore
	detectors       *DetectorRegistry
	publisher       EventPublisher
	enforcedAnchors map[string]bool // catalog Anchor ids for EvaluateBlock
	mcpBindingsFor  MCPBindingsForFunc
	mcpSchemaApply  MCPSchemaApplyFunc
	loader          *Loader
	factProviders   map[string]FactProvider
	advisorySink    AdvisorySink
}

// NewGuardPipeline wires the engine pieces into the runtime pipeline.
func NewGuardPipeline(rules *RuleSet, loader *Loader, counters *CounterStore) *GuardPipeline {
	if counters == nil {
		counters = NewCounterStore()
	}
	detectors := NewDetectorRegistry()
	if loader != nil {
		if reg := loader.Detectors(); reg != nil {
			detectors = reg
		}
	}
	return &GuardPipeline{
		rules:     rules,
		counters:  counters,
		detectors: detectors,
		publisher: unavailablePublisher{},
		loader:    loader,
	}
}

// SetDetectors replaces the detector registry.
func (p *GuardPipeline) SetDetectors(r *DetectorRegistry) {
	if p == nil {
		return
	}
	if r == nil {
		r = NewDetectorRegistry()
	}
	p.detectors = r
}

// SetEventPublisher installs the on_fire event sink.
func (p *GuardPipeline) SetEventPublisher(publisher EventPublisher) {
	if p == nil {
		return
	}
	if publisher == nil {
		publisher = unavailablePublisher{}
	}
	p.publisher = publisher
}

// Counters returns the engine counter store (tests / host sync).
func (p *GuardPipeline) Counters() *CounterStore {
	if p == nil {
		return nil
	}
	return p.counters
}

// Rules returns the device RuleSet installed at construction.
func (p *GuardPipeline) Rules() *RuleSet {
	if p == nil {
		return nil
	}
	return p.rules
}

// SetRuleSetFor installs a per-evaluation RuleSet resolver. Nil clears it.
func (p *GuardPipeline) SetRuleSetFor(fn RuleSetForFunc) {
	if p == nil {
		return
	}
	p.ruleSetFor = fn
}

func (p *GuardPipeline) effectiveRules(ctx context.Context, sessionID string) *RuleSet {
	if p == nil {
		return nil
	}
	if p.ruleSetFor != nil {
		if rs := p.ruleSetFor(ctx, sessionID); rs != nil {
			return rs
		}
	}
	return p.rules
}

// Evaluate runs rules for stage against gc. Ordering is kind then id.
func (p *GuardPipeline) Evaluate(ctx context.Context, stage Stage, gc *GuardContext) (*PipelineResult, error) {
	res := &PipelineResult{Enforced: true}
	if p == nil {
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
	publishOccurrenceAnchor(gc, "", p.capability())

	matched, selectionErrors := p.selectRules(rules, stage, "", gc)
	if err := p.evaluateMatched(ctx, holder, gc, matched, selectionErrors, stage, res); err != nil {
		return res, err
	}
	return res, nil
}

// evaluateMatched evaluates selected rules and resolves one Decision.
func (p *GuardPipeline) evaluateMatched(ctx context.Context, holder *evalHolder, gc *GuardContext, matched []*Rule, selectionErrors map[*Rule]error, stage Stage, res *PipelineResult) error {
	var blocks, transforms, nudges, warns []Decision
	var transformContributions []transformContribution
	// Suppressors run before their targets ([OAR-EVAL-18]).
	suppressed := map[string]bool{}
	ordered := orderBySuppression(matched)
	for i, r := range ordered {
		if suppressed[r.Qualified()] {
			// [OAR-EVAL-14] no decision, no side-effect, still traced.
			res.Trace.Add(TraceEntry{Rule: r.Qualified(), Stage: stage, Outcome: TraceSuppressed})
			continue
		}
		detectorState := captureDetectorFacts(gc)
		restoreRuleDetector := func() {
			if r.Kind == KindDetector {
				restoreDetectorFacts(gc, detectorState)
			}
		}
		outcome, err := TraceErrored, selectionErrors[r]
		if err == nil {
			outcome, err = p.evalRule(ctx, holder, r, gc)
		}
		if err != nil {
			restoreRuleDetector()
			// [OAR-EVAL-10] A monitor rule that raises is recorded
			// monitored_errored; its on_error is not applied.
			if r.Enforcement == "monitor" {
				res.Trace.Add(TraceEntry{Rule: r.Qualified(), Stage: stage, Outcome: TraceMonitoredErrored, Error: err.Error()})
				continue
			}
			res.Trace.Add(TraceEntry{Rule: r.Qualified(), Stage: stage, Outcome: TraceErrored, Error: err.Error()})
			d, stop, applyErr := p.applyOnError(r, gc, err)
			if applyErr != nil {
				return applyErr
			}
			if d != nil {
				blocks = append(blocks, *d)
				if stop {
					res.Decision = resolve(blocks, nil, nil, nil)
					traceSuppressedRemainder(res, ordered, i+1, suppressed, stage)
					finishContent(res, gc, nil)
					return nil
				}
			}
			continue
		}
		res.Trace.Add(TraceEntry{Rule: r.Qualified(), Stage: stage, Outcome: outcome, Effect: r.Effect})
		if observedOnly(outcome) {
			restoreRuleDetector()
			continue
		}
		d := decisionFromRule(r, gc, nil)
		ruleCounterKey := counterKeyFor(r, gc)
		var transformTarget any
		if r.Transform != nil {
			if r.Transform.Target != "content" {
				transformTarget = activation(gc)[r.Transform.Target]
			}
		}
		restoreRuleDetector()
		event := OnFireEvent{
			SessionID: gc.Session.SessionID,
			Rule:      r.Qualified(),
			Anchor:    r.Anchor,
			Effect:    r.Effect,
		}
		applied, sideEffectErr := ExecuteOnFire(ctx, p.counters, p.publisher, event, ruleCounterKey, r.OnFire)
		res.AppliedOnFire = append(res.AppliedOnFire, applied...)
		for _, action := range applied {
			if action == OnFirePublishEvent {
				res.PublishedEvents = append(res.PublishedEvents, event)
			}
		}
		if sideEffectErr != nil {
			last := len(res.Trace.Entries) - 1
			res.Trace.Entries[last].Outcome = TraceErrored
			res.Trace.Entries[last].Error = sideEffectErr.Error()
			d, stop, applyErr := p.applyOnError(r, gc, sideEffectErr)
			if applyErr != nil {
				return applyErr
			}
			if d != nil {
				blocks = append(blocks, *d)
				if stop {
					res.Decision = resolve(blocks, nil, nil, nil)
					traceSuppressedRemainder(res, ordered, i+1, suppressed, stage)
					finishContent(res, gc, nil)
					return nil
				}
			}
			continue
		}
		// [OAR-EVAL-14] only a firing enforce rule suppresses; [OAR-EVAL-10]
		// keeps a monitor rule from suppressing anything.
		if outcome == TraceFired {
			for _, t := range r.overrideTargets {
				suppressed[t] = true
			}
		}
		switch r.Effect {
		case EffectBlock:
			// [OAR-EVAL-4] the first enforced block short-circuits.
			// [OAR-OPS-10] already-suppressed rules are still recorded.
			blocks = append(blocks, d)
			res.Decision = resolve(blocks, nil, nil, nil)
			traceSuppressedRemainder(res, ordered, i+1, suppressed, stage)
			finishContent(res, gc, nil)
			return nil
		case EffectTransform:
			// Transforms accumulate until a block wins ([OAR-EVAL-5]).
			transforms = append(transforms, d)
			if r.Transform != nil {
				transformContributions = append(transformContributions, transformContribution{
					rule:       r.Qualified(),
					spec:       r.Transform,
					targetFact: transformTarget,
				})
			}
		case EffectNudge:
			nudges = append(nudges, d)
		case EffectWarn:
			warns = append(warns, d)
		case EffectAllow:
		}
	}
	res.Decision = resolve(blocks, transforms, nudges, warns)
	if res.Decision == nil || res.Decision.Effect != EffectTransform {
		transformContributions = nil
	}
	finishContent(res, gc, transformContributions)
	return nil
}

func (p *GuardPipeline) selectRules(rules *RuleSet, stage Stage, anchor string, gc *GuardContext) ([]*Rule, map[*Rule]error) {
	var out []*Rule
	errs := map[*Rule]error{}
	var src []*Rule
	if rules == nil {
		return nil, errs
	}
	if anchor != "" {
		src = rules.OnAnchor(anchor)
	} else {
		src = rules.All()
	}
	for _, r := range src {
		if enforcementOff(r) {
			continue
		}
		if StageFromAnchor(r.Anchor) != stage {
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

// evalRule reports the trace outcome. Fired is TraceFired or TraceMonitoredFired.
func (p *GuardPipeline) evalRule(ctx context.Context, holder *evalHolder, r *Rule, gc *GuardContext) (outcome TraceOutcome, err error) {
	// Flow precedes the potentially raising condition ([OAR-EVAL-2]).
	if len(r.Flow) > 0 && !FlowMatches(gc.Session.RecentToolNames, r.Flow) {
		return notFiredOutcome(r), nil
	}
	if err := p.maybeApplyMCPSchema(ctx, r.Anchor, []*Rule{r}, gc); err != nil {
		return TraceErrored, err
	}
	names := RuleFactsReferenced(r)
	if r.Kind == KindDetector {
		names = append([]string(nil), names...)
		kept := names[:0]
		for _, name := range names {
			if !isDetectorFact(name) {
				kept = append(kept, name)
			}
		}
		names = kept
	}
	if err := AssembleFacts(gc, names); err != nil {
		return TraceErrored, err
	}
	if holder != nil {
		holder.setCurrentRule(r)
	}
	key, err := holder.counterScope(r)
	if err != nil {
		return TraceErrored, err
	}
	gc.Counters.FireCount = holder.getCounter(gc.Session.SessionID, key, CounterFire)
	gc.Counters.BreakerCount = holder.getCounter(gc.Session.SessionID, key, CounterBreaker)
	if r.Kind == KindDetector {
		ref := ""
		if r.Detector != nil {
			ref = r.Detector.Ref
		}
		findings, derr := p.detectors.Dispatch(ref, gc)
		if derr != nil {
			return TraceErrored, derr
		}
		// Observation facts only — rule when defines thresholds / fire decision.
		ApplyFindings(gc, findings)
	}
	ok, err := evalRuleWhen(holder, r, gc)
	if err != nil {
		return TraceErrored, err
	}
	if !ok {
		return notFiredOutcome(r), nil
	}
	if r.Effect == EffectTransform {
		if err := validateFiredTransform(r, gc); err != nil {
			return TraceErrored, err
		}
	}
	if r.document != nil {
		if _, err := r.document.RenderCopy(activation(gc)); err != nil {
			return TraceErrored, err
		}
	}
	if r.Enforcement == "monitor" {
		return TraceMonitoredFired, nil
	}
	return TraceFired, nil
}

// enforcementOff reports a rule that must not be selected, evaluated, or traced
// ([OAR-EVAL-11]). It stays loaded, so a load-time error in it is still reported.
func enforcementOff(r *Rule) bool { return r.Enforcement == "off" }

// notFiredOutcome distinguishes a monitor rule that would not have fired from an
// enforcing one that did not ([OAR-OPS-9]).
func notFiredOutcome(r *Rule) TraceOutcome {
	if r.Enforcement == "monitor" {
		return TraceMonitoredPassed
	}
	return TracePassed
}

// observedOnly reports outcomes that contribute no decision and no side-effect:
// a rule that did not fire, and any monitor-mode rule ([OAR-EVAL-10]).
func observedOnly(o TraceOutcome) bool {
	return o == TracePassed || o == TraceMonitoredPassed || o == TraceMonitoredFired
}

func (p *GuardPipeline) applyOnError(r *Rule, gc *GuardContext, evalErr error) (d *Decision, stop bool, err error) {
	// [OAR-EVAL-10] A monitor rule's on_error is never applied.
	if r.Enforcement == "monitor" {
		return nil, false, nil
	}
	switch r.OnError {
	case "fail_open":
		return nil, false, nil
	case "fail_closed", "":
		// [OAR-OPS-3] fired block.
		data := map[string]any{"error": evalErr.Error()}
		dec := decisionFromRule(r, gc, data)
		dec.Effect = EffectBlock
		return &dec, true, nil
	default:
		if r.errorTarget == nil {
			return nil, false, fmt.Errorf("rule %s: %w", r.ID, evalErr)
		}
		dec := decisionFromRule(r.errorTarget, gc, map[string]any{"error": evalErr.Error()})
		dec.Effect = EffectBlock
		dec.OnFire = nil
		return &dec, true, nil
	}
}

// SetFactProvider binds a lazily produced host observation to its declared fact.
func (p *GuardPipeline) SetFactProvider(name string, provider FactProvider) {
	if p.factProviders == nil {
		p.factProviders = map[string]FactProvider{}
	}
	p.factProviders[name] = provider
}

func (p *GuardPipeline) registerFactProviders(gc *GuardContext) {
	for name, provider := range p.factProviders {
		if _, supplied := gc.lazy.providers[name]; !supplied {
			gc.RegisterProvider(name, provider)
		}
	}
}

func isDetectorFact(name string) bool {
	switch name {
	case "secret_matches", "pii_entities", "prompt_injection_score", "jailbreak_score", "moderation_category", "moderation_score":
		return true
	}
	return false
}
