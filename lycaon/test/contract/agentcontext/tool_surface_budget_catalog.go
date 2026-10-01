package contract

import (
	"path/filepath"
	"sort"
)

// toolSurfaceBudgetEntries documents per-profile caps for failure digests.
func toolSurfaceBudgetEntries(lycaonRoot string, ids []string) map[string]promptBudgetEntry {
	sort.Strings(ids)
	out := make(map[string]promptBudgetEntry, len(ids))
	for _, id := range ids {
		out[id] = promptBudgetEntry{
			Measures: "Wire-facing LLM tool definitions (name + description + parameters JSON) for profile " + id + "; deferred tools excluded, coordinator metas trimmed. Coordinator turn surfaces narrow further — this is the upper bound.",
			Fixture:  "executor.List(ToolFilter{ProfileID: " + id + "})",
			TrimPaths: []string{
				filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "schemas"),
				filepath.Join(lycaonRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles", id+".yaml"),
			},
			BumpNote: "Prefer demoting cold tools from 'sticky' to true in the profile (schemas then load on demand via request_tools) over bumping the cap.",
		}
	}
	return out
}
