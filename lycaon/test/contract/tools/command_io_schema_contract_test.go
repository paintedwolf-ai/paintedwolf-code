package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCommandToolSchemaIncludesIOParams(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	for _, name := range []string{"command", "verify"} {
		raw, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
		contractcheck.FailErr(t, "read tools/schemas/"+name, err)
		text := string(raw)
		for _, field := range []string{
			"stdin:",
			"stdin_from:",
			"env:",
			"stdout_to:",
			"stderr_to:",
			"append:",
		} {
			if !strings.Contains(text, field) {
				t.Fatalf("%s schema missing %q", name, field)
			}
		}
	}
}
