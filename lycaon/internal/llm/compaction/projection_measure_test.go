package compaction

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProjectionEnrichmentAccountsForAllDeliveredFields(t *testing.T) {
	before := []api.Message{{Role: "assistant", Content: strings.Repeat("observation ", 100)}}
	for _, after := range [][]api.Message{
		{{Role: "assistant", Content: "short continuation"}},
		before,
		{{Role: "assistant", Content: "short", ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"content": strings.Repeat("hidden argument ", 1000)}}}}},
	} {
		fits, err := ProjectionDoesNotGrow(SessionInfo{}, before, after)
		testutil.FailErr(t, "measure optional projection", err)
		want := len(after[0].ToolCalls) == 0
		if fits != want {
			t.Fatalf("projection fit = %v, want %v", fits, want)
		}
	}
}
