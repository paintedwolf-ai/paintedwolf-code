package findings

import "github.com/lycaon/lycaon/pkg/api"

// VisitFindingLocations visits primary and evidence locations without flattening call structure.
func VisitFindingLocations(finding *api.SecurityFinding, visit func(*api.SecurityFindingLocation)) {
	for i := range finding.Locations {
		visit(&finding.Locations[i])
	}
	if finding.Dataflow == nil {
		return
	}
	visitCallLocations(finding.Dataflow.Source, visit)
	for i := range finding.Dataflow.Intermediates {
		visit(&finding.Dataflow.Intermediates[i])
	}
	visitCallLocations(finding.Dataflow.Sink, visit)
}

func visitCallLocations(trace *api.SecurityFindingCallTrace, visit func(*api.SecurityFindingLocation)) {
	for trace != nil {
		visit(&trace.Location)
		for i := range trace.Intermediates {
			visit(&trace.Intermediates[i])
		}
		trace = trace.Callee
	}
}
