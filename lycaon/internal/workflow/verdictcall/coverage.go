package verdictcall

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// CheckCoverageIDs verifies the host can name its current facts in the offered
// contract before asking a model to submit them.
func CheckCoverageIDs(call map[string]any, loop workflowdef.ReviewLoopDef, facts reviewcoverage.Facts) error {
	props, _ := call["properties"].(map[string]any)
	verdict, _ := props["verdict"].(map[string]any)
	members, _ := verdict["properties"].(map[string]any)
	for field, kind := range loop.VerdictSchema {
		if kind != workflowdef.VerdictCoverageType {
			continue
		}
		coverage, _ := members[field].(map[string]any)
		fields, _ := coverage["properties"].(map[string]any)
		assessments, _ := fields["assessments"].(map[string]any)
		entry, _ := assessments["items"].(map[string]any)
		fields, _ = entry["properties"].(map[string]any)
		id, ok := fields["id"].(map[string]any)
		if !ok {
			return fmt.Errorf("coverage schema has no assessment id contract")
		}
		probe := map[string]any{"type": "object", "properties": map[string]any{"id": id}, "required": []any{"id"}}
		for _, fact := range append(append([]reviewcoverage.Fact(nil), facts.Obligations...), facts.Gaps...) {
			if err := tools.ValidateToolArgs(probe, map[string]any{"id": fact.ID}); err != nil {
				return fmt.Errorf("host coverage fact %q cannot satisfy the offered schema: %w", fact.ID, err)
			}
		}
	}
	return nil
}
