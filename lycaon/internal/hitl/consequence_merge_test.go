package hitl

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestMaxConsequenceNeverLowersBand(t *testing.T) {
	band, code := MaxConsequence(
		api.ConsequenceBandHighRisk, api.ConsequenceCodeDetection,
		api.ConsequenceBandStandard, "",
	)
	if band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeDetection {
		t.Fatalf("MaxConsequence = (%q, %q) want (high_risk, detection)", band, code)
	}
}

func TestMaxConsequenceRaisesBandAndPrefersSecretCode(t *testing.T) {
	band, code := MaxConsequence(
		api.ConsequenceBandStandard, "",
		api.ConsequenceBandHighRisk, api.ConsequenceCodeSecret,
	)
	if band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeSecret {
		t.Fatalf("MaxConsequence = (%q, %q) want (high_risk, secret)", band, code)
	}
	band, code = MaxConsequence(
		api.ConsequenceBandHighRisk, api.ConsequenceCodeDetection,
		api.ConsequenceBandHighRisk, api.ConsequenceCodeSecret,
	)
	if band != api.ConsequenceBandHighRisk || code != api.ConsequenceCodeSecret {
		t.Fatalf("MaxConsequence detection+secret = (%q, %q) want (high_risk, secret)", band, code)
	}
}
