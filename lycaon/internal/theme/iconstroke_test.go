package theme

import (
	"math"
	"testing"
)

func TestIconStrokeRejectsNonFiniteWeight(t *testing.T) {
	t.Parallel()

	for name, weight := range map[string]float64{
		"nan":          math.NaN(),
		"positive inf": math.Inf(1),
		"negative inf": math.Inf(-1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, faults := resolveIconStroke(&StrokeDeclaration{Weight: &weight})
			if len(faults) != 1 || faults[0].Field != "icons.stroke.weight" {
				t.Errorf("faults = %+v, want one weight fault", faults)
			}
		})
	}
}
