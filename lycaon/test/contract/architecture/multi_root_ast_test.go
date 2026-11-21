package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

// The cwd argument selects an attached root.
func TestMultiRootDisclosureCommandCwdIsTheOnlyRootSelector(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	for _, rootCount := range []int{0, 1, 2, 3} {
		metas := tools.ListToolsForProfile(context.Background(), exec.Metadata, platform.ToolFilter{
			ProfileID:        "implement",
			ProjectRootCount: rootCount,
		})
		for _, meta := range metas {
			if meta.Name != "command" {
				continue
			}
			props, _ := meta.ArgsSchema["properties"].(map[string]any)
			if props == nil {
				t.Fatalf("command schema properties missing at root count %d", rootCount)
			}
			if _, ok := props["root"]; ok {
				t.Fatalf("command schema advertises root at root count %d — args validate against the canonical schema, so an injected property is a reject trap", rootCount)
			}
			cwd, ok := props["cwd"].(map[string]any)
			if !ok {
				t.Fatalf("command schema missing cwd at root count %d", rootCount)
			}
			desc, _ := cwd["description"].(string)
			if !strings.Contains(desc, "@label") {
				t.Fatalf("command cwd description must teach @label addressing, got %q", desc)
			}
		}
	}
}
