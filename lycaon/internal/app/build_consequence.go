package app

import (
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/pkg/api"
)

// checkpointConsequenceDeriver is the single mint-site adapter for every
// tool-approval subject.
type checkpointConsequenceDeriver struct {
	dests approvals.ConsequenceBandPaths
}

func (d checkpointConsequenceDeriver) ToolApproval(detectionLevel string, secretScreenHit bool) (api.ConsequenceBand, api.ConsequenceCode) {
	bi := approvals.BandInput{
		Kind:            string(api.CheckpointKindToolApproval),
		DetectionLevel:  detectionLevel,
		SecretScreenHit: secretScreenHit,
	}
	if approvals.BandFor(bi, d.dests) != api.ConsequenceBandHighRisk {
		return "", ""
	}
	return api.ConsequenceBandHighRisk, approvals.ConsequenceCode(bi, d.dests)
}

func (d checkpointConsequenceDeriver) WriteRoot(proposedWriteRoot string) (api.ConsequenceBand, api.ConsequenceCode) {
	bi := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		ProposedWriteRoot: proposedWriteRoot,
	}
	if approvals.BandFor(bi, d.dests) != api.ConsequenceBandHighRisk {
		return "", ""
	}
	return api.ConsequenceBandHighRisk, approvals.ConsequenceCode(bi, d.dests)
}
