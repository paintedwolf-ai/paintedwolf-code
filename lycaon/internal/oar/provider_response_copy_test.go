package oar

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInvalidResponseUsesBoundedCatalogFeedback(t *testing.T) {
	ensureCatalog(t)
	loader, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rules, err := loader.LoadDir(hintsDir(t))
	testutil.FailErr(t, "rules", err)
	pipeline := NewGuardPipeline(rules, loader, NewCounterStore())
	pipeline.EnableAnchor(AnchorContentOutput)
	gc := NewGuardContext()
	gc.SetContentObservation(ContentObservation{InvalidResponse: true})
	result, err := pipeline.EvaluateBlock(context.Background(), AnchorContentOutput, gc)
	testutil.FailErr(t, "evaluate", err)
	if !result.Enforced || result.Decision == nil || result.Decision.Code != "MODEL_RESPONSE_INVALID" {
		t.Fatalf("result=%+v", result)
	}
	size := 0
	for _, text := range result.Decision.Copy {
		size += len(text)
	}
	if size > 600 || !strings.Contains(result.Decision.Copy["fix"], "requested format") || !strings.Contains(result.Decision.Copy["why"], "No tool calls") {
		t.Fatalf("copy=%+v", result.Decision.Copy)
	}
}
