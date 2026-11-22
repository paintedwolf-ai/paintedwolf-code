package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvidenceKindShapeMapUsesOnlyLockedShapes(t *testing.T) {
	t.Parallel()
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	for kind, shape := range binding.KindShapeMap() {
		switch shape {
		case evidence.ShapeFileRegion, evidence.ShapeURL, evidence.ShapeCommand, evidence.ShapeArtifact,
			evidence.ShapeVisual, evidence.ShapeSurfaceSnapshot, evidence.ShapePageGeometry, evidence.ShapeStructuredEvent:
		default:
			t.Fatalf("kind %q maps to unexpected shape %q", kind, shape)
		}
	}
}
