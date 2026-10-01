package oar

import (
	"context"
	"fmt"
)

// AdvisorySink delivers frozen policy results without reevaluation.
type AdvisorySink func(context.Context, string, string, *Decision) error

func (p *GuardPipeline) SetAdvisorySink(sink AdvisorySink) { p.advisorySink = sink }

func (p *GuardPipeline) deliverAdvisories(ctx context.Context, anchor string, gc *GuardContext, result *PipelineResult) error {
	if result.Decision == nil || (result.Decision.Effect != EffectWarn && result.Decision.Effect != EffectNudge) {
		return nil
	}
	if p.advisorySink == nil {
		return nil // Standalone evaluators return the decision to their caller.
	}
	if err := p.advisorySink(ctx, gc.SessionID, anchor, result.Decision); err != nil {
		return fmt.Errorf("deliver policy advisories at %s: %w", anchor, err)
	}
	return nil
}

// InlineAdvisory selects result warnings and parent-directed worker feedback.
func InlineAdvisory(anchor string, effect Effect) bool {
	if anchor == AnchorWorkerFinalize {
		return true
	}
	return effect == EffectWarn && (anchor == AnchorToolPost || anchor == AnchorCredentialAssignment)
}
