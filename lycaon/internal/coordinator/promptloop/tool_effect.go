package promptloop

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// committedEffect keeps a producer-stated no-op from clearing repetition history.
func (run completedToolRun) committedEffect() bool {
	if !run.contract.MutatesWorld() {
		return false
	}
	if run.call.Name != "update_progress" {
		return true
	}
	var result struct {
		Status string `json:"status"`
	}
	return json.Unmarshal([]byte(run.output), &result) == nil && result.Status == "updated"
}

// Progress documents can change without advancing a deliverable.
func progressResultAdvanced(output string) bool {
	var result struct {
		StateChanged   bool   `json:"state_changed"`
		Reconciliation string `json:"reconciliation_status"`
	}
	return json.Unmarshal([]byte(output), &result) == nil && (result.StateChanged || result.Reconciliation == "acknowledged")
}

type batchProgressOutcome struct{ Advanced, Blocked bool }

// Successful bookkeeping is neutral; only a refusal extends a blocked streak.
func classifyBatchProgress(history []api.Message, assistantMessageID string) batchProgressOutcome {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return batchProgressOutcome{Advanced: true}
	}
	result := batchProgressOutcome{}
	observed := false
	for _, msg := range history {
		receipt := msg.ToolResult
		if msg.Role != api.MessageRoleTool || receipt == nil || strings.TrimSpace(receipt.AssistantMessageID) != assistantMessageID {
			continue
		}
		observed = true
		if receipt.Outcome == api.ToolResultOutcomeRejected || receipt.Outcome == api.ToolResultOutcomeError {
			result.Blocked = true
			continue
		}
		if receipt.Tool != "update_progress" || progressResultAdvanced(receipt.Content) {
			result.Advanced = true
		}
	}
	if !observed {
		result.Advanced = true
	}
	return result
}
