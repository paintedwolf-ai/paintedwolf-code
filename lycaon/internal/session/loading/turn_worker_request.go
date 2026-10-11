package loading

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerRequest reads the assignment from its recorded job binding,
// before host-rendered operating instructions consume the model's input budget.
func (m *Service) WorkerRequest(sess *api.Session, jobID, openingID string, history []api.Message) (string, bool) {
	if jobID == "" {
		for _, msg := range history {
			if msg.ID == openingID {
				jobID = msg.WorkerID
				break
			}
		}
	}
	if m.workerQueue == nil || jobID == "" {
		return "", false
	}
	job, ok := m.workerQueue.Get(jobID)
	if !ok || job == nil || job.ChildSessionID != sess.ID || job.ParentSessionID != sess.ParentSessionID || job.AgentType != sess.AgentType {
		return "", false
	}
	// Delegation jobs keep the leg prompt; direct task jobs keep the charter
	// goal in Brief and a host-rendered assignment in Prompt.
	text := strings.TrimSpace(job.Brief)
	if job.LegID != "" {
		text = strings.TrimSpace(job.Prompt)
	}
	return text, text != ""
}
