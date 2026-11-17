package evidence

import "testing"

// A registered shape that falls through to the default is indistinguishable
// from a novel MCP shape, so every registered shape has an explicit tier.
func TestFidelityClassifiesEveryRegisteredShape(t *testing.T) {
	want := map[string]string{
		ShapeFileRegion:      FidelityStructured,
		ShapeArtifact:        FidelityStructured,
		ShapeVisual:          FidelityStructured,
		ShapePageGeometry:    FidelityStructured,
		ShapeStructuredEvent: FidelityStructured,
		ShapeURL:             FidelityScraped,
		ShapeSurfaceSnapshot: FidelityScraped,
		ShapeCommand:         FidelityOpaque,
		ShapeOpaque:          FidelityOpaque,
	}
	registered := DefaultShapeRegistry().shapes
	if len(registered) != len(want) {
		t.Fatalf("registered shapes = %d, classified = %d", len(registered), len(want))
	}
	for shape := range registered {
		expected, ok := want[shape]
		if !ok {
			t.Fatalf("registered shape %q has no declared trust tier", shape)
		}
		if got := FidelityForShape(shape); got != expected {
			t.Fatalf("fidelity for %q = %q want %q", shape, got, expected)
		}
	}
}

func TestFidelityForUnknownShapeIsOpaque(t *testing.T) {
	if got := FidelityForShape("custom_timeline"); got != FidelityOpaque {
		t.Fatalf("unknown shape trust tier = %q want %q", got, FidelityOpaque)
	}
}
