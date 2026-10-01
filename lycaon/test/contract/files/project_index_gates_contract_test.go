package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestNativeToolsBeforeMultiRootSandbox(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	// Multi-root sandbox depends on native path tools — boot registry must expose them.
	reg := toolfixture.ContractServeBootRegistry(t)
	registered := toolfixture.BootRegisteredToolSet(t, reg)
	for _, tool := range []string{"read", "grep", "find", "list_dir", "command"} {
		if !registered[tool] {
			t.Fatalf("native tool %q must be registered before multi-root sandbox", tool)
		}
	}
	if _, err := os.Stat(filepath.Join(lycaonRoot, "internal", "project", "roots.go")); err != nil {
		t.Fatalf("multi-root resolver missing: %v", err)
	}
}

func TestChildTableIdentityPresent(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schema, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "db", "schema.sql"))
	contractcheck.FailErr(t, "read schema.sql", err)
	if !strings.Contains(string(schema), "project_id") {
		t.Fatal("schema.sql must carry project_id on child tables")
	}
}
