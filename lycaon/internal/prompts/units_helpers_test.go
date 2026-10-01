package prompts_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/testutil"
)

// mergeUnits renders the unit slots a host template expects into vars, the
// way assembly does for a turn whose offered schemas are offered.
func mergeUnits(t *testing.T, engine prompts.PromptTemplateEngine, vars map[string]any, host promptunit.Host, mode string, offered []string) {
	t.Helper()
	catalog, err := prompts.UnitCatalogForEngine(engine)
	testutil.FailErr(t, "unit catalog", err)
	if offered == nil {
		offered, _ = vars["surface_offered"].([]string)
	}
	blocks, err := prompts.RenderUnitSlots(context.Background(), prompts.UnitRendererFor(engine), catalog, prompts.UnitSelectionVars(host, mode, offered, offered, nil), vars)
	testutil.FailErr(t, "render unit slots", err)
	prompts.MergeUnitVars(vars, blocks)
}
