package approvals

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// BandInput carries host facts collected before card presentation.
type BandInput struct {
	Kind              string // api.CheckpointKind
	DetectionLevel    string // Sigma level of the matched rule; "" when no match
	SecretScreenHit   bool   // the outbound secret screen matched this action
	ProposedWriteRoot string // reviewed write-root target; "" for other subjects
}

// BandFor classifies card presentation independently of authorization.
func BandFor(in BandInput, dests ConsequenceBandPaths) api.ConsequenceBand {
	if !bandKindInScope(in.Kind) {
		return api.ConsequenceBandStandard
	}
	if secretBandHit(in) || detectionBandHit(in) || writeRootBandHit(in, dests) {
		return api.ConsequenceBandHighRisk
	}
	return api.ConsequenceBandStandard
}

// ConsequenceCode returns the closed-set code for the Den consequence line, chosen by
// fixed priority secret → detection → write root. Empty when the band is standard.
func ConsequenceCode(in BandInput, dests ConsequenceBandPaths) api.ConsequenceCode {
	if BandFor(in, dests) != api.ConsequenceBandHighRisk {
		return ""
	}
	if secretBandHit(in) {
		return api.ConsequenceCodeSecret
	}
	if detectionBandHit(in) {
		return api.ConsequenceCodeDetection
	}
	if writeRootBandHit(in, dests) {
		return api.ConsequenceCodeWriteRoot
	}
	return ""
}

func bandKindInScope(kind string) bool {
	switch strings.TrimSpace(kind) {
	case string(api.CheckpointKindToolApproval):
		return true
	default:
		return false
	}
}

func secretBandHit(in BandInput) bool {
	return in.SecretScreenHit
}

func detectionBandHit(in BandInput) bool {
	return strings.EqualFold(strings.TrimSpace(in.DetectionLevel), "critical")
}

func writeRootBandHit(in BandInput, dests ConsequenceBandPaths) bool {
	if strings.TrimSpace(in.Kind) != string(api.CheckpointKindToolApproval) || strings.TrimSpace(in.ProposedWriteRoot) == "" {
		return false
	}
	return dests.Intersects(in.ProposedWriteRoot)
}
