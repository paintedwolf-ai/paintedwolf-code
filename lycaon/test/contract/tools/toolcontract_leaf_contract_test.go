package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPromptsUseCompiledToolContracts(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	promptsDir := filepath.Join(root, "lycaon", "internal", "prompts")
	varsPath := filepath.Join(promptsDir, "tool_profile_prompt_vars.go")
	src, err := os.ReadFile(varsPath)
	contractcheck.FailErr(t, "read tool_profile_prompt_vars.go", err)
	if !strings.Contains(string(src), "github.com/lycaon/lycaon/internal/toolcontract") {
		t.Fatal("tool_profile_prompt_vars.go must import internal/toolcontract")
	}
	if !strings.Contains(string(src), "toolcontract.Lookup") {
		t.Fatal("tool_profile_prompt_vars.go must call toolcontract.Lookup")
	}
}

func TestToolsHubUsesCompiledToolContracts(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "registry_impl.go")
	src, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read registry_impl.go", err)
	if !strings.Contains(string(src), "toolcontract.Lookup") {
		t.Fatal("registry_impl.go must resolve catalog contracts")
	}
}
