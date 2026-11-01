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
	rawVerify, err := os.ReadFile(filepath.Join(dir, "verify.yaml"))
	contractcheck.FailErr(t, "read tools/schemas/verify", err)
	verifyText := string(rawVerify)
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
	rawCommand, err := os.ReadFile(filepath.Join(dir, "command.yaml"))
	contractcheck.FailErr(t, "read tools/schemas/command", err)
	commandText := string(rawCommand)
	for _, field := range []string{
		"command:",
		"cwd:",
		"timeout_ms:",
		"capability_request:",
	} {
		if !strings.Contains(commandText, field) {
			t.Fatalf("command schema missing %q", field)
		}
	}
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
