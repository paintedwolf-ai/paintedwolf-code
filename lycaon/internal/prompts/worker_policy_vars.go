package prompts

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/spawn"
)

// MergeWorkerPolicyTemplateVars adds host policy caps for worker persona templates.
func MergeWorkerPolicyTemplateVars(agentID string, into map[string]any) error {
	if into == nil {
		return fmt.Errorf("nil template vars map")
	}
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("agent id required")
	}
	for k, v := range spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget()) {
		if _, exists := into[k]; !exists {
			into[k] = v
		}
	}
	for k, v := range progress.ProgressTemplateVars() {
		if _, exists := into[k]; !exists {
			into[k] = v
		}
	}
	return nil
}
