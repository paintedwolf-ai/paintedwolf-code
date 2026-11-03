package contract

import (
	"strings"
	"testing"
)

func TestPlatformEvidenceCapturesEachTargetBeforeTheNext(t *testing.T) {
	t.Parallel()
	steps := hostedWorkflowFile(t, "platform-verification").Jobs["upgrade-corpus"].Steps
	corpus, corpusEvidence, confinement, confinementEvidence := -1, -1, -1, -1
	launch, launchEvidence := -1, -1
	for index, step := range steps {
		switch step.ID {
		case "launch":
			launch = index
		case "corpus":
			corpus = index
		case "confinement":
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
			if step.With["status"] != "${{ steps.corpus.outcome }}" {
				t.Error("corpus evidence must use its own step outcome")
			}
		}
		if strings.HasPrefix(step.With["name"], "confinement-linux-launch-amd64-") {
			launchEvidence = index
			if step.With["status"] != "${{ steps.launch.outcome }}" {
				t.Error("launch evidence must use its own step outcome")
			}
		}
		if strings.HasPrefix(step.With["name"], "confinement-linux-amd64-") {
			confinementEvidence = index
			if step.With["status"] != "${{ steps.confinement.outcome }}" {
				t.Error("confinement evidence must use its own step outcome")
			}
		}
	}
	if !(launch >= 0 && launch < launchEvidence && launchEvidence < corpus && corpus < corpusEvidence && corpusEvidence < confinement && confinement < confinementEvidence) {
		t.Fatalf("platform evidence must precede the next target: corpus %d/%d, confinement %d/%d", corpus, corpusEvidence, confinement, confinementEvidence)
	}
}
