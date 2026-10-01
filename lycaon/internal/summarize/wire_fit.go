package summarize

// WireFitEstimator estimates the complete response size.
type WireFitEstimator func(Result) int

// wireFitMinPackBudget preserves identity and coverage rollups.
const wireFitMinPackBudget = 300

// fitWire trims the lowest-utility rows to the wire budget.
func (e *Engine) fitWire(req Request, res Result) Result {
	wireBudget := e.Caps.Pack.WireBudgetTokens
	if e.WireFit == nil || wireBudget <= 0 {
		return res
	}
	est := e.WireFit(res)
	if est <= wireBudget {
		return res
	}

	trimSteps := 0
	for trimSteps < wireFitMaxTrimSteps && est > wireBudget {
		if !TrimPackOneStep(res.Task, &res.Pack) {
			break
		}
		trimSteps++
		noteWireFitGap(&res.Pack)
		res.Anchors = pickPackAnchors(res.Pack, e.Caps, req.MaxAnchors)
		res.Coverage.AnchorsReturned = len(res.Anchors)
		est = e.WireFit(res)
	}

	res.Orchestration.Curator.WireFitPasses = trimSteps
	return res
}

func noteWireFitGap(pack *ContextPack) {
	if pack == nil {
		return
	}
	note := "pack trimmed for wire budget (task-scored retention)"
	for _, g := range pack.Gaps {
		if g == note {
			return
		}
	}
	pack.Gaps = append(pack.Gaps, note)
}
