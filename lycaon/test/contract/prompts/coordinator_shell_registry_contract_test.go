package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var coordinatorFamilyShellTopics = map[string][]string{
	"coordinator-mode-shell-investigate.md": {
		"coordinator-trust-bundle-tiers.md",
	},
	"coordinator-mode-shell-orchestrate.md": {
		"coordinator-mode-orchestrate-invariants-edits.md",
		"coordinator-mode-orchestrate-pacing.md",
		"coordinator-mode-orchestrate-cycle.md",
	},
}

func TestCoordinatorFamilyShellRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	partialsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "partials")
	for shell, topics := range coordinatorFamilyShellTopics {
		shellPath := filepath.Join(partialsDir, shell)
		raw, err := os.ReadFile(shellPath)
		if err != nil {
			t.Fatalf("read shell %s: %v", shell, err)
		}
		body := string(raw)
		if !strings.Contains(body, "{{ units.execution }}") {
			t.Fatalf("shell %s does not place the execution unit slot", shell)
		}
		for _, topic := range topics {
			if !strings.Contains(body, topic) {
				t.Fatalf("shell %s missing include of %s", shell, topic)
			}
			if _, err := os.Stat(filepath.Join(partialsDir, topic)); err != nil {
				t.Fatalf("topic file missing %s: %v", topic, err)
			}
		}
	}
}
