package extensionadmin

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// The rejection carries the retired name and its successor as typed facts.
func TestRetiredToolDetailsCarryTheRecordedSuccessor(t *testing.T) {
	for _, tc := range []struct {
		retired *toolcontract.RetiredToolError
		want    map[string]any
	}{
		{&toolcontract.RetiredToolError{Site: `profile "p" grant`, Tool: "state_update", Retirement: toolcontract.Retirement{Successors: []string{"workflow_advance", "workflow_transition"}}},
			map[string]any{"retired_tool": "state_update", "retired_site": `profile "p" grant`, "successor": []string{"workflow_advance", "workflow_transition"}}},
		{&toolcontract.RetiredToolError{Site: `rule "R" selector.tool`, Tool: "state_query", Retirement: toolcontract.Retirement{ContentContract: "paintedwolf.workflow_run_context"}},
			map[string]any{"retired_tool": "state_query", "retired_site": `rule "R" selector.tool`, "successor_contract": "paintedwolf.workflow_run_context"}},
	} {
		if got := retiredToolDetails(tc.retired); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("details = %v, want %v", got, tc.want)
		}
	}
}
