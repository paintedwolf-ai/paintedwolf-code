package toolfeedback

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolcommand"
)

func TestGlobRecoveryRetainsTheProducerBound(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, maximum := range []int{0, 37, 1000} {
		observation := toolcommand.ArgvShapeObservation("command", &commandsurface.GlobBudgetError{
			Pattern: "**/*.go", Limit: "matches", Max: maximum,
		})
		err := bp.RejectObservation(t.Context(), "command", "fixture", nil, observation)
		refusal, ok := guidance.RefusalFromError(err)
		if !ok {
			t.Fatalf("missing glob refusal: %v", err)
		}
		if !strings.Contains(refusal.Copy["what"], fmt.Sprintf("Maximum: %d matches", maximum)) {
			t.Fatalf("producer bound lost: %#v", refusal.Copy)
		}
		if strings.TrimSpace(refusal.Copy["fix"]) != "" || strings.TrimSpace(refusal.Copy["instead"]) != "" {
			t.Fatalf("glob refusal invented a pattern rewrite: %#v", refusal.Copy)
		}
	}
}
