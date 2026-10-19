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
