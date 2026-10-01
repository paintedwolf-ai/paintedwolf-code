package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSharedObservationFactsRegistered(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "oar")
	envSrc, err := os.ReadFile(filepath.Join(root, "env.go"))
	contractcheck.FailErr(t, "read env.go", err)
	envBody := string(envSrc)
	// The lazy-assembly set is derived from the declaration table rather than
	// written out beside it, so ask the live catalogue for membership and keep
	// the source grep only for the activation map env.go actually builds.
	declared := map[string]bool{}
	for _, f := range oar.FactCatalogue() {
		declared[f.Name] = true
	}
	for _, name := range []string{
		"is_directory", "not_found", "path_denied", "reject_observation",
		"policy_denied", "command_not_argv",
	} {
		if !declared[name] {
			t.Errorf("catalogue missing %q", name)
		}
		if !strings.Contains(envBody, `"`+name+`"`) {
			t.Errorf("condition environment missing %q", name)
		}
	}
}
