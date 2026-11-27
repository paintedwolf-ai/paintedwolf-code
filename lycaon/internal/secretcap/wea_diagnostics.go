package secretcap

import (
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

const weaMaxSecretDiagChecks = 64

// observeScreenedEchoWEA runs a bounded post-screen custody check on the output
// of Resolution.ReferenceEchoes. On healthy runs where every secret was rewritten
// to its reference token, zero findings are emitted. Raw secret bytes are never
// included in emitted findings. When diagnostics are disabled, this returns
// immediately with zero allocations.
func (r *Resolution) observeScreenedEchoWEA(screened string) bool {
	if !cmddiag.Enabled() || r == nil || len(r.values) == 0 || screened == "" {
		return false
	}
	checked := 0
	var leakedID, leakedDigest string
	for id, v := range r.values {
		if checked >= weaMaxSecretDiagChecks {
			break
		}
		checked++
		if v.value == "" {
			continue
		}
		if strings.Contains(screened, v.value) {
			leakedID = id
			leakedDigest = cmddiag.DigestReference(v.value)
			break
		}
	}
	rec := cmddiag.WEARecordedArtifactFact{
		RecordID:              cmddiag.DigestReference("screened_echo", leakedID),
		RecordKind:            "screened_echo",
		ScreenedReferenceOnly: leakedID == "",
		CanaryDetected:        leakedID != "",
		MatchedCanaryID:       leakedID,
		SourceRef:             cmddiag.DigestReference("secretcap", "reference_echoes"),
	}
	obs := cmddiag.WEAObservation{
		ScopeKey:          rec.RecordID,
		Settled:           true,
		RecordedArtifacts: []cmddiag.WEARecordedArtifactFact{rec},
	}
	if leakedID != "" {
		obs.SecretCanaries = []cmddiag.WEASecretCanaryFact{
			{
				CanaryID:     leakedID,
				Scope:        "resolution",
				CanaryDigest: leakedDigest,
			},
		}
	}
	return cmddiag.ObserveWEA(obs)
}
