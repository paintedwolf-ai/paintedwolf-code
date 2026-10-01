package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestWorkflowAgentsDeclareToolsPolicy(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflowsRoot := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf")
	err := filepath.WalkDir(workflowsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "workflow.yaml" {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var doc struct {
			Agents []struct {
				ID    string `yaml:"id"`
				Tools string `yaml:"tools"`
			} `yaml:"agents"`
		}
		if yaml.Unmarshal(raw, &doc) != nil {
			return nil
		}
		for _, agent := range doc.Agents {
			if strings.TrimSpace(agent.Tools) == "" {
				t.Errorf("%s: agent %q missing tools policy", path, agent.ID)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk workflows", err)
}
