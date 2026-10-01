package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestAgentProfileToolsExcludeDisallowedProfile(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	names := toolfixture.SortedToolNames(context.Background(), exec, "worker_readonly")
	for _, name := range names {
		if name == "command" {
			t.Fatal("worker_readonly profile must not list command")
		}
	}
	if len(names) == 0 {
		t.Fatal("expected worker_readonly to allow read-only tools")
	}
}
