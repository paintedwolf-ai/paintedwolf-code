package orchestration

import (
	"fmt"
	"regexp"
)

var pipelineStageName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// validatePipelineStages checks stage names are unique and input_from references exist.
func validatePipelineStages(stages []PipelineStage) error {
	if len(stages) == 0 {
		return fmt.Errorf("pipeline has no stages")
	}
	names := make(map[string]struct{}, len(stages))
	for _, stage := range stages {
		if stage.Name == "" {
			return fmt.Errorf("pipeline stage missing name")
		}
		if !pipelineStageName.MatchString(stage.Name) {
			return fmt.Errorf("pipeline stage %q: name must contain only lowercase letters, digits, underscores, or hyphens", stage.Name)
		}
		if _, dup := names[stage.Name]; dup {
			return fmt.Errorf("duplicate pipeline stage %q", stage.Name)
		}
		names[stage.Name] = struct{}{}
	}
	for _, stage := range stages {
		for _, pred := range stage.InputFrom {
			if _, ok := names[pred]; !ok {
				return fmt.Errorf("pipeline stage %q input_from references unknown stage %q", stage.Name, pred)
			}
		}
	}
	return nil
}

// stageReady reports whether all predecessor stages are complete.
func stageReady(stage PipelineStage, completed map[string]bool) bool {
	for _, pred := range stage.InputFrom {
		if !completed[pred] {
			return false
		}
	}
	return true
}

// readyPipelineStages returns stages whose dependencies are satisfied and that are not yet complete.
func readyPipelineStages(stages []PipelineStage, completed map[string]bool) []PipelineStage {
	var ready []PipelineStage
	for _, stage := range stages {
		if completed[stage.Name] {
			continue
		}
		if stageReady(stage, completed) {
			ready = append(ready, stage)
		}
	}
	return ready
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
