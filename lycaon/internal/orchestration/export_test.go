package orchestration

import "fmt"

// RunIDForDelegationForTest observes an admitted run while synchronous Run is blocked.
func RunIDForDelegationForTest(o *OrchestratorImpl, delegationID string) string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for id, state := range o.runs {
		if state.delegationID == delegationID {
			return id
		}
	}
	return ""
}

// pipelineDispatchOrder returns stage names in a valid topological order for tests.
func pipelineDispatchOrder(stages []PipelineStage) ([]string, error) {
	if err := validatePipelineStages(stages); err != nil {
		return nil, err
	}
	completed := make(map[string]bool, len(stages))
	order := make([]string, 0, len(stages))
	for len(order) < len(stages) {
		ready := readyPipelineStages(stages, completed)
		if len(ready) == 0 {
			return nil, fmt.Errorf("pipeline stage graph has a cycle")
		}
		for _, stage := range ready {
			order = append(order, stage.Name)
			completed[stage.Name] = true
		}
	}
	return order, nil
}

// pipelineMustFollow returns true when stage b must complete before stage a can dispatch.
func pipelineMustFollow(stages []PipelineStage, before, after string) bool {
	completed := map[string]bool{before: true}
	for _, stage := range stages {
		if stage.Name != after {
			continue
		}
		return stageReady(stage, completed)
	}
	return false
}
