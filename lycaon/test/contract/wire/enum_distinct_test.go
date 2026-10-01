package contract

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestAllStatusEnumsAreDistinct(t *testing.T) {
	t.Parallel()
	assertDistinct(t, "SessionStatus", []string{
		string(api.SessionStatusIdle),
		string(api.SessionStatusBusy),
		string(api.SessionStatusError),
	})
	assertDistinct(t, "BlueprintStatus", []string{
		string(api.BlueprintStatusDraft),
		string(api.BlueprintStatusApproved),
		string(api.BlueprintStatusImplementing),
		string(api.BlueprintStatusDone),
	})
	// WorkerStatus and WorkerSummaryStatus have canonical All…() registries;
	// derive from them so a newly added constant can't skip the distinctness
	// check by being forgotten in a hand-maintained list here.
	assertDistinct(t, "WorkerSummaryStatus", enumStrings(api.AllWorkerSummaryStatuses()))
	assertDistinct(t, "WorkerStatus", enumStrings(api.AllWorkerStatuses()))
	assertDistinct(t, "LegStatus", []string{
		string(api.LegStatusPending),
		string(api.LegStatusDispatched),
		string(api.LegStatusRunning),
		string(api.LegStatusComplete),
		string(api.LegStatusFailed),
		string(api.LegStatusHeld),
	})
	assertDistinct(t, "CodeScanStatus", []string{
		string(api.CodeScanStatusPending),
		string(api.CodeScanStatusRunning),
		string(api.CodeScanStatusComplete),
		string(api.CodeScanStatusFailed),
		string(api.CodeScanStatusTimedOut),
		string(api.CodeScanStatusCanceled),
		string(api.CodeScanStatusSuperseded),
	})
	assertDistinct(t, "SessionPosture", []string{
		string(api.SessionPostureSpec),
		string(api.SessionPostureBuild),
		string(api.SessionPostureOrchestrate),
		string(api.SessionPostureVet),
	})
}

// enumStrings converts a slice of string-kinded enum values to plain strings so
// a distinctness check can consume an All…() registry directly.
func enumStrings[T ~string](vals []T) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out
}

func assertDistinct(t *testing.T, name string, values []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v == "" {
			t.Fatalf("%s: empty value", name)
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("%s: duplicate value %q", name, v)
		}
		seen[v] = struct{}{}
	}
}
