package contract

// Tool-name allowances must name tools that exist.

import (
	"sort"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// toolNameAllowances lists maps keyed by tool name.
func toolNameAllowances() map[string]map[string]string {
	return map[string]map[string]string{
		"coordinatorPromptlessAllowlist": coordinatorPromptlessAllowlist,
	}
}

func TestToolNameAllowancesNameLiveTools(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	known := knownToolUniverse(t, root)
	if len(known) == 0 {
		t.Fatal("derived an empty tool universe — every allowance below would read as stale")
	}

	var stale []string
	for label, allowance := range toolNameAllowances() {
		for tool, rationale := range allowance {
			// A wildcard stands for a family the registry enumerates per install
			// (mcp_*), so there is no single name to resolve.
			if isToolFamilyWildcard(tool) {
				continue
			}
			if _, live := known[tool]; !live {
				stale = append(stale, label+"["+tool+"] is not a live tool — drop the allowance "+
					"(its rationale claims: "+rationale+")")
			}
		}
	}
	sort.Strings(stale)
	contractcheck.FailViolations(t, "tool-name allowances for tools the registry does not have", stale)
}

// isToolFamilyWildcard reports a name that stands for a per-install family
// rather than one registered tool.
func isToolFamilyWildcard(tool string) bool {
	return len(tool) > 0 && tool[len(tool)-1] == '*'
}
