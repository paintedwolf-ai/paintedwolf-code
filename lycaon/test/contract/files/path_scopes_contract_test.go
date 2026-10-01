package contract

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPathScopesYAMLLoads(t *testing.T) {
	t.Parallel()
	reg, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "load path-scopes.yaml", err)
	if len(reg) == 0 {
		t.Fatal("expected path scopes")
	}
}

func TestCoordinatorProfileUsesProductReadScope(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfgRoot := filepath.Join(root, "lycaon")
	data, err := os.ReadFile(filepath.Join(cfgRoot, "config", "packs", "painted-wolf", "platform", "tools", "profiles", "coordinator.yaml"))
	contractcheck.FailErr(t, "read file", err)
	if !containsLine(data, "read_scope: coordinator_product_read") {
		t.Fatal("coordinator profile must reference read_scope: coordinator_product_read")
	}
	if !containsLine(data, "write_scope: coordinator_orchestration") {
		t.Fatal("coordinator profile must reference write_scope: coordinator_orchestration")
	}
	if containsLine(data, "read_globs:") || containsLine(data, "write_globs:") {
		t.Fatal("coordinator profile must not inline read_globs/write_globs — use path-scopes.yaml")
	}

	scopes, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "load path-scopes.yaml", err)
	prof, err := sandbox.ParseToolProfile(data, scopes)
	contractcheck.FailErr(t, "sandbox.ParseToolProfile failed", err)
	wantRead := scopes["coordinator_product_read"].Read
	wantWrite := append([]string(nil), scopes["coordinator_orchestration"].Write...)
	if !slices.Equal(prof.ReadGlobs, wantRead) {
		t.Fatalf("resolved read_globs = %v want %v", prof.ReadGlobs, wantRead)
	}
	if !slices.Equal(prof.WriteGlobs, wantWrite) {
		t.Fatalf("resolved write_globs = %v want %v", prof.WriteGlobs, wantWrite)
	}
}

func containsLine(data []byte, needle string) bool {
	return slices.Contains(splitLines(string(data)), needle)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	return out
}
