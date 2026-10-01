package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tooloutput"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// TestAgentWireSpillPathFixturesAreRelative locks catalog scenario spill_path
// values to host-data-relative wire form (docs/host-contract.md § Agent-visible
// host spill paths). Absolute ~/.config/paintedwolf or /tmp/host fixtures are forbidden.
func TestAgentWireSpillPathFixturesAreRelative(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packs := filepath.Join(root, "lycaon", "config", "packs")
	var bad []string
	err := filepath.WalkDir(packs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil // non-object YAML unrelated
		}
		scen, _ := doc["scenarios"].([]any)
		for _, item := range scen {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			data, _ := obj["data"].(map[string]any)
			if data == nil {
				continue
			}
			spill, _ := data["spill_path"].(string)
			if spill == "" {
				continue
			}
			if !tooloutput.IsAgentWireSpillRel(spill) {
				rel, _ := filepath.Rel(root, path)
				bad = append(bad, rel+": spill_path="+spill)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk packs", err)
	if len(bad) > 0 {
		t.Fatalf("catalog spill_path fixtures must be host-data-relative:\n%s", strings.Join(bad, "\n"))
	}
}
