package summarize

import "context"

type outlineObservation struct {
	structure StructureCandidate
	ok        bool
}

// curatorOutliner shares admission and memoization across directory branches.
type curatorOutliner struct {
	provider     OutlineProvider
	files        int
	names        int
	fileLimit    int
	nameLimit    int
	observations map[string]outlineObservation
}

func (o *curatorOutliner) Outline(ctx context.Context, path string) (StructureCandidate, bool) {
	if prior, ok := o.observations[path]; ok {
		return prior.structure, prior.ok
	}
	if o.fileLimit > 0 && o.files >= o.fileLimit {
		return StructureCandidate{}, false
	}
	o.files++
	sc, ok := o.provider.Outline(ctx, path)
	o.observations[path] = outlineObservation{structure: sc, ok: ok}
	return sc, ok
}

func (o *curatorOutliner) indexOutline(ctx context.Context, path string) (StructureCandidate, bool) {
	if prior, ok := o.observations[path]; ok {
		return prior.structure, prior.ok
	}
	if o.names >= o.nameLimit {
		return StructureCandidate{}, false
	}
	o.names++
	return o.Outline(ctx, path)
}
