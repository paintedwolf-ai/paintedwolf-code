package contract

import (
	"strings"
	"testing"
)

func TestPlatformEvidenceCapturesEachTargetBeforeTheNext(t *testing.T) {
	t.Parallel()
	steps := hostedWorkflowFile(t, "platform-verification").Jobs["upgrade-corpus"].Steps
	corpus, corpusEvidence, confinement, confinementEvidence := -1, -1, -1, -1
	for index, step := range steps {
		switch step.Run {
		case "./task upgrade:corpus:boot":
			corpus = index
		case "./task test:confine:linux":
			confinement = index
		}
		if step.Uses != "./.github/actions/verification-evidence" {
			continue
		}
		if step.If != "always()" {
			t.Error("platform evidence must survive target failures")
		}
		if strings.HasPrefix(step.With["name"], "upgrade-corpus-") {
			corpusEvidence = index
		}
		if strings.HasPrefix(step.With["name"], "confinement-linux-amd64-") {
			confinementEvidence = index
			if step.With["status"] != "${{ steps.confinement.outcome }}" {
				t.Error("confinement evidence must use its own step outcome")
			}
		}
	}
	if !(corpus >= 0 && corpus < corpusEvidence && corpusEvidence < confinement && confinement < confinementEvidence) {
		t.Fatalf("platform evidence must precede the next target: corpus %d/%d, confinement %d/%d", corpus, corpusEvidence, confinement, confinementEvidence)
	}
}
