package promptloop

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/trainingcapture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTheCapturePolicyAsksForTheSurfaceOfThisSessionAndRequest(t *testing.T) {
	var gotSession string
	var gotOffered []string
	reader := testCoordinatorReader(t, "read")
	turn := &modelTurn{Projection: &turnProjection{}, Context: &promptContext{}}
	if policy := turn.trainingCapturePolicy(t.Context(), &api.Session{ID: "s1"}, reader); policy.Surface != nil {
		t.Fatal("a surface hook appeared without a surface dependency")
	}
	turn.Context.Deps.PromptSurface = func(_ context.Context, sess *api.Session, r prompts.Reader) (trainingcapture.PromptSurface, error) {
		gotSession, gotOffered = sess.ID, r.Offered()
		return trainingcapture.PromptSurface{TurnLoadReceiptID: 3}, nil
	}
	policy := turn.trainingCapturePolicy(t.Context(), &api.Session{ID: "s1"}, reader)
	surface, err := policy.Surface(t.Context(), []string{"read"})
	testutil.FailErr(t, "surface", err)
	if gotSession != "s1" || !slices.Equal(gotOffered, []string{"read"}) || surface.TurnLoadReceiptID != 3 {
		t.Fatalf("surface request = %q %v", gotSession, gotOffered)
	}
	if _, err := turn.trainingCapturePolicy(t.Context(), &api.Session{ID: "s1"}, prompts.Reader{}).Surface(t.Context(), nil); err == nil {
		t.Fatal("an unconstructed request reader reported a surface")
	}
}
