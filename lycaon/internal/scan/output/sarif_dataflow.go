package output

import (
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/owenrumney/go-sarif/v2/sarif"
)

func sarifDataflow(flow *api.SecurityFindingDataflow) []*sarif.CodeFlow {
	if flow == nil {
		return nil
	}
	thread := &sarif.ThreadFlow{}
	appendStep := func(location api.SecurityFindingLocation, kind string, depth int) {
		thread.Locations = append(thread.Locations, &sarif.ThreadFlowLocation{
			Kinds: []string{kind}, NestingLevel: intPtr(depth),
			Location: &sarif.Location{PhysicalLocation: &sarif.PhysicalLocation{
				ArtifactLocation: &sarif.ArtifactLocation{URI: &location.URI},
				Region:           &sarif.Region{StartLine: intPtr(location.StartLine), StartColumn: intPtr(location.StartColumn), EndLine: intPtr(location.EndLine), EndColumn: intPtr(location.EndColumn)},
			}},
		})
	}
	var source func(*api.SecurityFindingCallTrace, int)
	source = func(call *api.SecurityFindingCallTrace, depth int) {
		if call == nil {
			return
		}
		if call.Callee == nil {
			appendStep(call.Location, "source", depth)
			return
		}
		source(call.Callee, depth+1)
		for _, step := range call.Intermediates {
			appendStep(step, "propagation", depth+1)
		}
		appendStep(call.Location, "return", depth)
	}
	source(flow.Source, 0)
	for _, step := range flow.Intermediates {
		appendStep(step, "propagation", 0)
	}
	depth := 0
	for call := flow.Sink; call != nil; call = call.Callee {
		kind := "call"
		if call.Callee == nil {
			kind = "sink"
		}
		appendStep(call.Location, kind, depth)
		depth++
		for _, step := range call.Intermediates {
			appendStep(step, "propagation", depth)
		}
	}
	if len(thread.Locations) == 0 {
		return nil
	}
	return []*sarif.CodeFlow{{ThreadFlows: []*sarif.ThreadFlow{thread}}}
}
