package promptloop

import "encoding/json"

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
		StateChanged bool `json:"state_changed"`
		Reconciled   bool `json:"reconciled"`
	}
	return json.Unmarshal([]byte(output), &result) == nil && (result.StateChanged || result.Reconciled)
}
