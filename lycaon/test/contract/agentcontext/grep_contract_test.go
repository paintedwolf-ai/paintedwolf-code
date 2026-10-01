package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestGrepInvalidRegexStructuredReject(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	exec := toolfixture.ContractToolExecutor(t)
	exec.SetToolSchemas(schemas)

	_, err = exec.Invoke(context.Background(), "grep", map[string]any{
		"pattern": "[unclosed",
	}, tools.ToolContext{
		Roots:              []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
		ActiveRootID:       "r1",
		Agent:              "implement",
		RepoFileCount:      100,
		RepoFileCountKnown: true,
	})
	if err == nil {
		t.Fatal("expected grep regex reject")
	}
	assertStructuredRejectCode(t, err, "GREP_REGEX_INVALID")
}

func TestGrepAlternationMatchesByDefault(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	exec := toolfixture.ContractToolExecutor(t)
	exec.SetToolSchemas(schemas)

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "secrets.txt"), []byte("token=1\n"), 0o644); err != nil {
		contractcheck.FailErr(t, "write file", err)
	}

	out, err := exec.Invoke(context.Background(), "grep", map[string]any{
		"pattern": "password|secret|token",
		"path":    ".",
	}, tools.ToolContext{
		Roots:              []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmp, IsPrimary: true}},
		ActiveRootID:       "r1",
		Agent:              "explore_readonly",
		RepoFileCount:      100,
		RepoFileCountKnown: true,
	})
	contractcheck.FailErr(t, "grep alternation default regex", err)
	if !strings.Contains(out, "token=1") {
		t.Fatalf("alternation should match without a regex flag: %q", out)
	}
}

func TestGrepSchemaHasRegexFields(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "LoadSchemaDir failed", err)
	entry, ok := schemas.Tools["grep"]
	if !ok {
		t.Fatal("grep missing from tools/schemas")
	}
	props, _ := entry.Schema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("grep schema missing properties")
	}
	for _, key := range []string{"case_insensitive", "max_matches", "context_lines"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("grep schema missing property %q", key)
		}
	}
	// Text search is RE2 unconditionally: a literal/regex mode flag is the one
	// arg an agent can set wrong and get a silent empty result from.
	if _, ok := props["regex"]; ok {
		t.Fatal("grep schema must not offer a regex mode flag")
	}
}
