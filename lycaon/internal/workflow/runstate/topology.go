package runstate

import (
	"strings"
)

func MarkTopologyStage(vars map[string]any, stage, output string) map[string]any {
	vars = CloneVars(vars)
	stages, _ := vars["topology_stages"].(map[string]any)
	if stages == nil {
		stages = map[string]any{}
		vars["topology_stages"] = stages
	}
	entry := map[string]any{"complete": true}
	output = strings.TrimSpace(output)
	if output != "" {
		entry["output"] = output
		vars = SetHostVar(vars, "topology_outputs."+stage, output)
	}
	stages[stage] = entry
	return vars
}
