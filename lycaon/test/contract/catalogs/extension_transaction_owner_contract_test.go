package contract

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// extpacksDoorFile commits package-cache metadata only.
const extpacksDoorFile = "cache_metadata.go"

// Extension state commits through the subsystem owner's atomic replace.
func TestExtpacksKeepsNoStateWriter(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	pkgDir := filepath.Join(root, "lycaon", "internal", "extpacks")
	entries, err := os.ReadDir(pkgDir)
	contractcheck.FailErr(t, "read internal/extpacks", err)

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == extpacksDoorFile {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, parser.ImportsOnly)
		contractcheck.FailErr(t, "parse "+name, err)
		for _, imp := range file.Imports {
			if strings.Contains(imp.Path.Value, "internal/fseffect") {
				t.Fatalf("%s imports fseffect — state commits belong to the subsystem owner", name)
			}
		}
	}

	// Cache metadata cannot reach desired or lock state.
	doorSource, err := os.ReadFile(filepath.Join(pkgDir, extpacksDoorFile))
	contractcheck.FailErr(t, "read "+extpacksDoorFile, err)
	for _, forbidden := range []string{"DesiredPathForScope", "LockPathForScope"} {
		if strings.Contains(string(doorSource), forbidden) {
			t.Fatalf("%s reaches %s — state paths belong to the subsystem owner", extpacksDoorFile, forbidden)
		}
	}

	// Extension state writer APIs are forbidden.
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(pkgDir, name))
		contractcheck.FailErr(t, "read "+name, err)
		for _, forbidden := range []string{"WriteDesiredFile", "WriteLockFile", "atomicWriteExtensionFile"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s references forbidden writer %s", name, forbidden)
			}
		}
	}
}
