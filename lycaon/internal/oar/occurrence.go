package oar

import (
	"github.com/lycaon/lycaon/internal/oarcore"
)

// publishOccurrenceAnchor sets the paintedwolf.anchor fact for this
// occurrence: the core spelling when the local catalog id implements a core
// anchor ([OAR-PROF-1], [OAR-CONF-3]), otherwise the local id.
func publishOccurrenceAnchor(gc *GuardContext, local string, cap *EngineDeclaration) {
	if gc == nil {
		return
	}
	if local == "" {
		local = gc.Anchor
	}
	if cap == nil {
		cap = InstalledEngineDeclaration()
	}
	if IsCoreAnchor(local) {
		gc.Anchor = local
		return
	}
	if core := cap.CoreAnchorFor(local); core != "" {
		gc.Anchor = core
		return
	}
	if local != "" {
		gc.Anchor = local
	}
}

func (p *GuardPipeline) declaration() *EngineDeclaration {
	if p != nil && p.loader != nil {
		return p.loader.declaration()
	}
	return InstalledEngineDeclaration()
}

// validateFiredTransform checks a transform rule's replacement target. A rule
// without x-paintedwolf-transform still decides transform; it names no content
// to replace.
func validateFiredTransform(r *Rule, gc *GuardContext) error {
	if r == nil || r.Transform == nil {
		return nil
	}
	facts := activation(gc)
	contentLen := 0
	if gc != nil {
		contentLen = len([]rune(gc.Content.Content))
	}
	return oarcore.ValidateTransformTarget(r.Transform.Target, facts, contentLen)
}

type transformContribution struct {
	rule       string
	spec       *TransformSpec
	targetFact any
}

func finishContent(res *PipelineResult, gc *GuardContext, contributions []transformContribution) {
	if res == nil {
		return
	}
	if len(contributions) == 0 {
		res.Transforms = nil
	} else {
		res.Transforms = make([]*TransformSpec, 0, len(contributions))
		for _, contribution := range contributions {
			res.Transforms = append(res.Transforms, contribution.spec)
		}
	}
	if gc == nil || !gc.Content.ContentSet {
		return
	}
	res.ContentSet = true
	if len(contributions) == 0 {
		res.Content = gc.Content.Content
		return
	}
	res.Content, res.SkippedTransforms = applyRuleTransforms(gc.Content.Content, contributions)
}

func applyRuleTransforms(content string, contributions []transformContribution) (string, []oarcore.SkippedTransform) {
	ruleIDs := make([]string, 0, len(contributions))
	converted := make([]*oarcore.TransformSpec, 0, len(contributions))
	targetFacts := make([]any, 0, len(contributions))
	for _, contribution := range contributions {
		spec := contribution.spec
		if spec == nil {
			continue
		}
		converted = append(converted, &oarcore.TransformSpec{
			Action:         spec.Action,
			Target:         spec.Target,
			Replacement:    spec.Replacement,
			HasReplacement: spec.HasReplacement,
		})
		targetFacts = append(targetFacts, contribution.targetFact)
		ruleIDs = append(ruleIDs, contribution.rule)
	}
	return oarcore.ApplyTransforms(content, converted, targetFacts, ruleIDs)
}
