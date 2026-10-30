package wiring

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// The composed host policy prunes wait's condition vocabulary to the
// profile's closed set, so the schema never offers conditions it refuses.
func TestComposedPolicyPrunesWaitConditionsToTheProfile(t *testing.T) {
	h := BuildForTest(t)
	ctx := context.Background()
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	prof, err := h.AgentRegistry.Get("implementer")
	testutil.FailErr(t, "agents.Get", err)
	for _, meta := range h.Sessions.Manager.Coordinator.Guards.Policy().ListForPrompt(ctx, sess, prof.ToolProfile) {
		if meta.Name != "wait" {
			continue
		}
		properties, _ := meta.ArgsSchema["properties"].(map[string]any)
		conditions, _ := properties["conditions"].(map[string]any)
		items, _ := conditions["items"].(map[string]any)
		itemProperties, _ := items["properties"].(map[string]any)
		kind, _ := itemProperties["kind"].(map[string]any)
		if got := fmt.Sprint(kind["enum"]); got != "[process_done http_ready port_ready]" {
			t.Fatalf("wait conditions = %s, want the implement profile's vocabulary", got)
		}
		return
	}
	t.Fatal("implementer schema lacks wait")
}
