package workerrecords

import (
	"encoding/json"
	"strings"
)

// ReviewSubject is the sealed result a reviewer reads: the result, the round
// that produced it, its worker and the checkpoint its changes are.
type ReviewSubject struct {
	ResultID     string `json:"result_id"`
	WorkerID     string `json:"worker_id"`
	JobID        string `json:"job_id"`
	CheckpointID string `json:"checkpoint_id"`
}

// Encode is the subject as a round stores it.
func (s ReviewSubject) Encode() (json.RawMessage, error) {
	return json.Marshal(s)
}

// Subject reads the review subject a reviewer's round carries.
func (r Round) Subject() (ReviewSubject, bool) {
	if strings.TrimSpace(r.ReviewSubject) == "" {
		return ReviewSubject{}, false
	}
	var s ReviewSubject
	if err := json.Unmarshal([]byte(r.ReviewSubject), &s); err != nil || s.ResultID == "" || s.CheckpointID == "" {
		return ReviewSubject{}, false
	}
	return s, true
}
