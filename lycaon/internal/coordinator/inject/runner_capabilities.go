package inject

import "github.com/lycaon/lycaon/internal/prompts"

func mergeRunnerCapabilityVars(offered []string, vars map[string]any) {
	prompts.MergeRunnerCapabilityVars(offered, vars)
}
