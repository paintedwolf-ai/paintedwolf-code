package tools

import "github.com/lycaon/lycaon/pkg/api"

// ConsequenceDeriver computes presentation-only bands at checkpoint mint from facts
// the mint site already holds. Wired in app from approvals.BandFor.
type ConsequenceDeriver interface {
	ToolApproval(detectionLevel string, secretScreenHit bool) (api.ConsequenceBand, api.ConsequenceCode)
	WriteRoot(proposedWriteRoot string) (api.ConsequenceBand, api.ConsequenceCode)
}
