package oar

// resolve picks the winning effect. Nudge and warn collect every live
// contribution of that effect in evaluation order ([OAR-EVAL-6],
// [OAR-EVAL-19], [OAR-EVAL-20]). Block, transform, and none have an empty
// advisory list.
func resolve(blocks, transforms, nudges, warns []Decision) *Decision {
	if len(blocks) > 0 {
		d := blocks[0]
		d.Advisories = nil
		return &d
	}
	if len(transforms) > 0 {
		d := transforms[0]
		d.Advisories = nil
		return &d
	}
	if len(nudges) > 0 {
		return advisoryDecision(EffectNudge, nudges)
	}
	if len(warns) > 0 {
		return advisoryDecision(EffectWarn, warns)
	}
	return nil
}

func advisoryDecision(effect Effect, hits []Decision) *Decision {
	d := hits[0]
	d.Effect = effect
	d.Advisories = make([]Advisory, len(hits))
	for i, h := range hits {
		d.Advisories[i] = Advisory{
			Code: h.Code,
			Rule: h.Rule,
			Copy: h.Copy,
			Data: h.Data,
		}
	}
	return &d
}
