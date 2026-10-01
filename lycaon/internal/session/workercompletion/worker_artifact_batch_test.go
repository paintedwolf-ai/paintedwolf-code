package workercompletion

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestArtifactProofPairsResultWithItsMutationCall(t *testing.T) {
	for _, tc := range []struct {
		name, firstID, secondID     string
		mutationID, mutationTool    string
		firstOutcome, secondOutcome api.ToolResultOutcome
		want                        bool
	}{
		{"successful read and rejected edit", "read-1", "edit-1", "edit-1", "edit", api.ToolResultOutcomeCompleted, api.ToolResultOutcomeRejected, false},
		{"rejected edit before successful read", "edit-1", "read-1", "edit-1", "edit", api.ToolResultOutcomeRejected, api.ToolResultOutcomeCompleted, false},
		{"successful edit after read", "read-1", "edit-1", "edit-1", "edit", api.ToolResultOutcomeCompleted, api.ToolResultOutcomeCompleted, true},
		{"successful restore_version", "read-1", "restore-1", "restore-1", "restore_version", api.ToolResultOutcomeCompleted, api.ToolResultOutcomeCompleted, true},
		{"unknown and missing result identities", "unknown", "", "edit-1", "edit", api.ToolResultOutcomeCompleted, api.ToolResultOutcomeCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []api.Message{
				{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "read-1", Name: "read"}, {ID: tc.mutationID, Name: tc.mutationTool}}},
				{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: tc.firstID, Outcome: tc.firstOutcome}},
				{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: tc.secondID, Outcome: tc.secondOutcome}},
			}
			if got := childHadSuccessfulMutationTool(messages); got != tc.want {
				t.Fatalf("mutation proof = %v, want %v", got, tc.want)
			}
		})
	}
}
