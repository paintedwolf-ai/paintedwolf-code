package toolexecution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestReviewedInputsAdmitActualNormalizedSizeBeforeEncoding(t *testing.T) {
	admission := stageMemory(t, 16384)
	original, err := prepareNativeArguments(t.Context(), admission, map[string]any{"path": "short"})
	testutil.FailErr(t, "admit original expansion", err)
	defer original.close()
	initialHeld := admission.InUse().State
	// A host normalization is larger than the admitted short original. It must
	// acquire its actual shape before canonical JSON or a recorder view exists.
	normalized := map[string]any{"path": strings.Repeat("expanded", 4096)}
	snapshot, reviewed, err := admitReviewedOperationInputs(t.Context(), admission, normalized)
	if !stateRefusal(err) || snapshot != nil || reviewed != (toolcontract.ReviewedOperationInputs{}) || admission.InUse().State != initialHeld {
		t.Fatalf("normalized input escaped admission: %v held%+v want%d", err, admission.InUse(), initialHeld)
	}
}

func TestReviewedInputsFreezeFinalObjectAndReleaseEncodingFailure(t *testing.T) {
	admission := stageMemory(t, 64<<20)
	input := map[string]any{"path": "reviewed", "large_integer": json.Number("9007199254740993")}
	snapshot, reviewed, err := admitReviewedOperationInputs(t.Context(), admission, input)
	testutil.FailErr(t, "freeze final review", err)
	input["path"] = "caller changed"
	if snapshot.values["path"] != "reviewed" || !strings.Contains(reviewed.CanonicalArguments(), `["utf8","reviewed"]`) || !strings.Contains(reviewed.CanonicalArguments(), "9007199254740993") || len(reviewed.ArgumentsDigest()) != 64 {
		t.Fatal("final reviewed object lost immutable value identity")
	}
	snapshot.close()
	if admission.InUse().State != 0 {
		t.Fatal("reviewed state lease leaked")
	}
	failed, view, err := admitReviewedOperationInputs(t.Context(), admission, map[string]any{"invalid": json.Number("invalid")})
	if err == nil || failed != nil || view != (toolcontract.ReviewedOperationInputs{}) || admission.InUse().State != 0 {
		t.Fatal("failed canonical encoding retained a view or lease")
	}
}
