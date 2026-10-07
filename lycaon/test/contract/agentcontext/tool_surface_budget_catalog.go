package contract

import (
	"path/filepath"
	"sort"
)

// toolSurfaceBudgetEntries documents per-profile tool surfaces for failure digests.
func toolSurfaceBudgetEntries(lycaonRoot string, ids []string) map[string]promptBudgetEntry {
	sort.Strings(ids)
	out := make(map[string]promptBudgetEntry, len(ids))
	for _, id := range ids {
		out[id] = promptBudgetEntry{
			Measures: "Wire-facing LLM tool definitions (name + description + parameters JSON) for profile " + id + "; deferred tools excluded, coordinator metas trimmed. Coordinator turn surfaces narrow further — this is the upper bound.",
			Fixture:  "executor.List(ToolFilter{ProfileID: " + id + "})",
			TrimPaths: []string{
				checkoutPath(lycaonRoot, filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas")),
				checkoutPath(lycaonRoot, filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles", id+".yaml")),
			},
			Advice: "Change cold tools from sticky to true in the profile so their schemas load on demand through request_tools.",
		}
	}
	return out
}
