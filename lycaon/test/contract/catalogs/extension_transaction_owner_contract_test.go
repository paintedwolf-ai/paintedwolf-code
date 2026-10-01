package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Extension handlers submit one typed transaction intent.
var forbiddenHandlerSelectors = map[string]string{
	"PrepareInstall":            "candidate preparation belongs to the subsystem owner",
	"PrepareInstallMeta":        "candidate preparation belongs to the subsystem owner",
	"PrepareRemoval":            "candidate preparation belongs to the subsystem owner",
	"PrepareUpdate":             "candidate preparation belongs to the subsystem owner",
	"PrepareReload":             "candidate preparation belongs to the subsystem owner",
	"PrepareLockDesired":        "candidate preparation belongs to the subsystem owner",
	"ApplyMutationToState":      "state mutation belongs to the subsystem owner",
	"PrepareMetaMemberMutation": "state mutation belongs to the subsystem owner",
	"EncodeDesired":             "state persistence belongs to the subsystem owner",
	"EncodeLock":                "state persistence belongs to the subsystem owner",
	"AcquireIntentLocks":        "the commit window belongs to the subsystem owner",
	"WithOmitted":               "omission attribution belongs to catalogview",
}

func TestExtensionHandlersSubmitOneIntent(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	apiDir := filepath.Join(root, "lycaon", "internal", "api")
	entries, err := os.ReadDir(apiDir)
	contractcheck.FailErr(t, "read internal/api", err)

	fset := token.NewFileSet()
	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "extensions") || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(apiDir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		contractcheck.FailErr(t, "parse "+name, err)
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "extpacks" {
				return true
			}
			if why, forbidden := forbiddenHandlerSelectors[sel.Sel.Name]; forbidden {
				violations = append(violations,
					name+": extpacks."+sel.Sel.Name+" — "+why)
			}
			return true
		})
	}
	if len(violations) > 0 {
		t.Fatalf("extension handlers must submit intents to the subsystem owner:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

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
