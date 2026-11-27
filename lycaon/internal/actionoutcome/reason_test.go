package actionoutcome

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestReasonsCoverGeneratedVocabulary(t *testing.T) {
	for _, reason := range api.AllWakeReasonValues() {
		if reasonState(reason) == "" {
			t.Errorf("no wake state for %q", reason)
		}
		if reasonState(reason) == api.WakeStateDeferred {
			continue
		}
		result := WakeResult(reason)
		if result.Reason == "" && result.FailureCode == "" {
			t.Errorf("no public result for %q", reason)
		}
		if reason != api.WakeReasonDefect && result.FailureCode == string(api.HostContinuationFailureHostInvariantFailed) {
			t.Errorf("unmapped wake reason %q", reason)
		}
	}
}
