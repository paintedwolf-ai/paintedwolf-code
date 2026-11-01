package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestAgentProfileToolsExcludeDisallowedProfile(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	names := toolfixture.SortedToolNames(context.Background(), exec.Metadata, "worker_readonly")
	hasCommand := false
	for _, name := range names {
		if name == "write" {
			t.Fatal("worker_readonly profile must not list write")
		}
		if name == "command" {
			hasCommand = true
		}
	}
	if !hasCommand {
		t.Fatal("worker_readonly profile must list read-only command")
	}
	if len(names) == 0 {
		t.Fatal("expected worker_readonly to allow read-only tools")
	}
}
