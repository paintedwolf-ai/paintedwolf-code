package spawn

import "github.com/lycaon/lycaon/pkg/api"

// WorkflowWork carries host-owned dispatch constraints for an active phase.
type WorkflowWork struct {
	RunID        string
	Phase        string
	AgentType    string
	Scope        *api.TaskScope
	Charter      *api.WorkerTaskCharter
	MaxToolLoops int
}
