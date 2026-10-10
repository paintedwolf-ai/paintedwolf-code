package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestHostCommandToolsShareRunner(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	mustContain := []struct {
		path    string
		markers []string
	}{
		{
			path: filepath.Join(root, "lycaon", "internal", "tools", "native", "command", "command_tool.go"),
			markers: []string{
				"CommandTool",
			},
		},
		{
			// Both command and verify foreground dispatch through the same helper, which builds a
			// hostcmd.Request and hands it to the bgprocess await/handle model.
			path: filepath.Join(root, "lycaon", "internal", "tools", "native", "command", "command_exec.go"),
			markers: []string{
				"hostcmd.Request",
				"registry.StartPipeline",
				"bgprocess.JobModeAwaited",
			},
		},
		{
			// Execution itself lives in bgprocess, which drives internal/exec async pipelines.
			path: filepath.Join(root, "lycaon", "internal", "bgprocess", "pipeline.go"),
			markers: []string{
				"exec.StartPipelineAsync",
				"exec.ExecOpts",
			},
		},
	}
	for _, tc := range mustContain {
		data, err := os.ReadFile(tc.path)
		contractcheck.FailErr(t, "read "+tc.path, err)
		text := string(data)
		for _, marker := range tc.markers {
			if !strings.Contains(text, marker) {
				t.Fatalf("%s missing %q", tc.path, marker)
			}
		}
	}
}
