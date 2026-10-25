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
	verifyRaw, err := os.ReadFile(filepath.Join(dir, "verify.yaml"))
	contractcheck.FailErr(t, "read tools/schemas/verify", err)
	verifyText := string(verifyRaw)
	for _, field := range []string{
		"stdin:",
		"stdin_from:",
		"env:",
		"stdout_to:",
		"stderr_to:",
		"append:",
	} {
		if !strings.Contains(verifyText, field) {
			t.Fatalf("verify schema missing %q", field)
		}
	}

	commandRaw, err := os.ReadFile(filepath.Join(dir, "command.yaml"))
	contractcheck.FailErr(t, "read tools/schemas/command", err)
	commandText := string(commandRaw)
	for _, retired := range []string{
		"pipeline:",
		"stdin:",
		"stdin_from:",
		"env:",
		"stdout_to:",
		"stderr_to:",
		"append:",
		"background:",
		"wait_ms:",
		"allow_concurrent:",
	} {
		if strings.Contains(commandText, "\n    "+retired) {
			t.Fatalf("command schema still contains retired argv field %q", retired)
		}
	}
}
