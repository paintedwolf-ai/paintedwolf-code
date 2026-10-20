package toolexecution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestReviewedInputsAdmitActualNormalizedSizeBeforeEncoding(t *testing.T) {
	cause := errors.New("host aggregate ceiling")
	held := int64(0)
	admission := programStateFunc(func(_ context.Context, n int64) (func(), error) {
		if n > 16384-held {
			return nil, cause
		}
		held += n
		return func() { held -= n }, nil
	})
	original, err := prepareNativeArguments(t.Context(), admission, map[string]any{"path": "short"})
	testutil.FailErr(t, "admit original expansion", err)
	defer original.close()
	initialHeld := held
	// A host normalization is larger than the admitted short original. It must
	// acquire its actual shape before canonical JSON or a recorder view exists.
	normalized := map[string]any{"path": strings.Repeat("expanded", 4096)}
	snapshot, reviewed, err := admitReviewedOperationInputs(t.Context(), admission, normalized)
	if !errors.Is(err, cause) || snapshot != nil || reviewed != (toolcontract.ReviewedOperationInputs{}) || held != initialHeld {
		t.Fatalf("normalized input escaped admission: %v held%d want%d", err, held, initialHeld)
	}
}

func TestReviewedInputsFreezeFinalObjectAndReleaseEncodingFailure(t *testing.T) {
	admission := &stageAdmissionFixture{}
	input := map[string]any{"path": "reviewed", "large_integer": json.Number("9007199254740993")}
	snapshot, reviewed, err := admitReviewedOperationInputs(t.Context(), admission, input)
	testutil.FailErr(t, "freeze final review", err)
	input["path"] = "caller changed"
	if snapshot.values["path"] != "reviewed" || !strings.Contains(reviewed.CanonicalArguments, `"path":"reviewed"`) || !strings.Contains(reviewed.CanonicalArguments, "9007199254740993") || len(reviewed.ArgumentsDigest) != 64 {
		t.Fatal("final reviewed object lost immutable value identity")
	}
	snapshot.close()
	if admission.held != 0 {
		t.Fatal("reviewed state lease leaked")
	}
	failed, view, err := admitReviewedOperationInputs(t.Context(), admission, map[string]any{"invalid": json.Number("invalid")})
	if err == nil || failed != nil || view != (toolcontract.ReviewedOperationInputs{}) || admission.held != 0 {
		t.Fatal("failed canonical encoding retained a view or lease")
	}
}
