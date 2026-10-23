package projectremoval

import wire "github.com/lycaon/lycaon/pkg/api"

func (o *Owner) live(projectID, operationID string) (wire.ProjectRemovalResult, bool) {
	o.activeMu.RLock()
	defer o.activeMu.RUnlock()
	result, ok := o.active[operationID]
	return result, ok && result.ProjectID == projectID
}

func (o *Owner) track(result wire.ProjectRemovalResult) {
	o.activeMu.Lock()
	defer o.activeMu.Unlock()
	if o.active == nil {
		o.active = make(map[string]wire.ProjectRemovalResult)
	}
	result.Progress = &wire.ProjectRemovalProgress{Phase: "preparing"}
	o.active[result.OperationID] = result
}

func (o *Owner) progress(operationID string, progress wire.ProjectRemovalProgress) {
	o.activeMu.Lock()
	defer o.activeMu.Unlock()
	result, ok := o.active[operationID]
	if !ok {
		return
	}
	result.Progress = &progress
	o.active[operationID] = result
}

func (o *Owner) forget(operationID string) {
	o.activeMu.Lock()
	defer o.activeMu.Unlock()
	delete(o.active, operationID)
}
