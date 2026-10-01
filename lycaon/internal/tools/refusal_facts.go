package tools

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/pkg/api"
)

// ApplyRefusalFacts copies confine codes and the observation onto completed facts.
func ApplyRefusalFacts(facts guidance.ToolResultFacts, stamped confine.StampedRefusal) guidance.ToolResultFacts {
	for _, code := range stamped.GuidanceCodes {
		if code == isolation.CodeRemotePackageDestinationDenied {
			details := map[string]any{"destination": stamped.Observation.Destination}
			facts = facts.WithFeedback(code, details, &api.FeedbackSubject{
				Kind: "destination", ID: stamped.Observation.Destination,
			})
			continue
		}
		facts = facts.WithCode(code)
	}
	facts.Confine = stamped.Observation
	return facts
}
