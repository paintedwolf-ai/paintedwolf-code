package promptloop

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/trainingcapture"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTheCapturePolicyAsksForTheSurfaceOfThisSessionAndProfile(t *testing.T) {
	var gotSession, gotProfile string
	var gotOffered []string
	turn := &modelTurn{Projection: &turnProjection{}, Context: &promptContext{}}
	if policy := turn.trainingCapturePolicy(t.Context(), &api.Session{ID: "s1"}, "implement"); policy.Surface != nil {
		t.Fatal("a surface hook appeared without a surface dependency")
	}
	turn.Context.Deps.PromptSurface = func(_ context.Context, sess *api.Session, profileID string, offered []string) (trainingcapture.PromptSurface, error) {
		gotSession, gotProfile, gotOffered = sess.ID, profileID, offered
		return trainingcapture.PromptSurface{TurnLoadReceiptID: 3}, nil
	}
	policy := turn.trainingCapturePolicy(t.Context(), &api.Session{ID: "s1"}, "implement")
	surface, err := policy.Surface(t.Context(), []string{"read"})
	testutil.FailErr(t, "surface", err)
	if gotSession != "s1" || gotProfile != "implement" || !slices.Equal(gotOffered, []string{"read"}) || surface.TurnLoadReceiptID != 3 {
		t.Fatalf("surface request = %q %q %v", gotSession, gotProfile, gotOffered)
	}
}
