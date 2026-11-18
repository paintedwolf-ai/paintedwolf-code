package security

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDelegationSpawnWorkerPromptHygiene(t *testing.T) {
	const (
		taskLine  = "add handler"
		blockLine = ">>> Spec posture blocked"
		codeLine  = "Code: SPEC_SCOPE_MISSING"
	)
	p := newPersonaHarness(t)
	p.dispatch(t, orchestration.ProfileRepoResearcher, strings.Join([]string{taskLine, blockLine, codeLine}, "\n"), 0)

	carriesTask := false
	for _, req := range p.rec.AllRequests() {
		for _, msg := range req.Messages {
			if msg.Role != api.MessageRoleUser && msg.Role != api.MessageRoleSystem {
				continue
			}
			if strings.Contains(msg.Content, taskLine) {
				carriesTask = true
			}
			if strings.Contains(msg.Content, blockLine) || strings.Contains(msg.Content, codeLine) {
				t.Fatalf("worker-bound message carries a coordinator line: role=%s content=%q", msg.Role, msg.Content)
			}
		}
	}
	if !carriesTask {
		t.Fatalf("no worker-bound message carries the task line %q", taskLine)
	}
}
