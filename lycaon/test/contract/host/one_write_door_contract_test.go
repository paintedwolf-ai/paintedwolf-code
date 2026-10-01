package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// doorExemptions identifies writes with lifetimes outside replacement transactions.
var doorExemptions = map[string]string{
	// Content-addressed stores determine the destination after streaming.
	"lycaon/internal/sourceblob/store.go": "stream-then-name: the object's destination is its digest",

	// Directory transactions promote and roll back whole trees.
	"lycaon/internal/detectionpack/import.go":         "directory promotion with rollback of the replaced pack tree",
	"lycaon/internal/extpacks/meta_install.go":        "directory promotion with backup and rollback",
	"lycaon/internal/extpacks/meta_transaction.go":    "recoverable directory promotion with backup and rollback",
	"lycaon/internal/extpacks/package_transaction.go": "directory promotion of a staged package tree",
	"lycaon/internal/project/promotion_engine.go":     "reservation-based promotion: renames are the transaction, not a write",
	"lycaon/internal/workspace/seed.go":               "directory promotion of a fully materialized seed tree",

	// An external writer creates this staging file.
	"lycaon/internal/db/store_schema.go": "SQLite VACUUM INTO writes the staged snapshot; the door uses its staging file",

	// Developer tooling writes user-named export files outside any managed tree.
	"lycaon/cmd/lycaon-debug/decide.go": "offline training exports to a path the developer names; nothing durable in the config tree",

	// The door stages a whole replacement; it cannot express an append.
	"lycaon/internal/blueprint/manager_impl.go": "append-only critic evidence log",
	"lycaon/internal/debugretention/writer.go":  "bounded append stream truncates in place at a configured diagnostic cap",
	"lycaon/internal/inspector/jsonl_store.go":  "append-only evidence journal",

	// O_EXCL reports collisions without replacing existing bytes.
	"lycaon/internal/api/owner_only_posix.go":             "O_EXCL create so the 0600 mode is in force before any byte lands",
	"lycaon/internal/api/owner_only_windows.go":           "owner-only ACL applied to a freshly created manifest",
	"lycaon/internal/filelock/filelock.go":                "lock file opened for the flock it carries, not for its contents",
	"lycaon/internal/fileclone/clone_linux.go":            "O_EXCL destination for a reflink clone; the ioctl replaces the write",
	"lycaon/internal/indexwatch/indexwatch.go":            "O_EXCL destination while copying index file",
	"lycaon/internal/promptattach/docext/worker.go":       "O_EXCL request file handed to a one-shot extraction worker",
	"lycaon/internal/scan/drivers/bundled/opengrep_io.go": "O_EXCL report path inside a scan run directory removed on cleanup",
	"lycaon/internal/workspace/overlay_copy.go":           "O_EXCL destination while materializing a fresh overlay tree",

	// Copies into a staging tree the caller promotes or discards whole.
	"lycaon/internal/backup/stage.go":                  "streams into an exclusive staging tree the caller promotes",
	"lycaon/internal/extpacks/install.go":              "copies a source tree into staging before package_transaction promotes it",
	"lycaon/internal/workspacebaseline/materialize.go": "rebuilds an unpublished branch under its provision lock; SnapshotComplete publishes the assembled tree",

	// Desktop trash relocations and spec metadata outside workspace transactions.
	"lycaon/internal/desktoptrash/trash_darwin_nocgo.go": "moves deleted files to user ~/.Trash directory when cgo is unavailable",
	"lycaon/internal/desktoptrash/trash_linux.go":        "implements FreeDesktop Trash specification by writing .trashinfo and relocating to files directory",

	// These paths have no prior content to preserve.
	"lycaon/internal/browserengine/provision.go": "copies a downloaded browser tree into a fresh provisioning directory, then publishes a completion marker",
	"lycaon/internal/decide/bialy/provision.go":  "renames each digest-verified checkpoint file into its model directory, then publishes a completion marker",
	"lycaon/internal/hostlock/hostlock.go":       "advisory pid stamp written under the lock it names; best-effort by design",
	"lycaon/internal/preflight/probes.go":        "writability probe: written to prove the mount accepts a write, then removed",
	"lycaon/cmd/lycaon-perf/fixtures.go":         "generated disposable benchmark tree under a fresh scratch root; no durable or prior content exists",
	"lycaon/internal/testdbfixture/open.go":      "clones cached database template into disposable test fixture path",

	// Maintenance commands write generated checkout artifacts.
	"lycaon/internal/approvalregistry/registry.go": "generated registry artifact written by ./task codegen:approval-explanations",
	"lycaon/internal/oar/conformance.go":           "seeds the conformance fixture corpus into a caller-named output directory",
	"lycaon/internal/oar/integrity.go":             "generated guidance registry written by ./task codegen:guidance-registry",

	// CLI output failures are returned to the operator.
	"lycaon/cmd/lycaon-debug/eval_tool_usage.go": "operator-selected report path",
	"lycaon/cmd/lycaon/completion.go":            "shell completion script written to an operator-selected path",
	"lycaon/cmd/lycaon/extensions_theme.go":      "theme export written to an operator-selected path",
	"lycaon/cmd/oar-import-invariant/main.go":    "invariant import written to an operator-selected path",
	"lycaon/cmd/decide-rerank/eval.go":           "evaluation report written to an operator-selected path",
	"lycaon/cmd/decide-rerank/units.go":          "training units written to an operator-selected path",
}

var doorPrimitives = map[string]bool{"Rename": true, "WriteFile": true, "Create": true}

func TestDurableWritesGoThroughOneDoor(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offences []string
	unused := map[string]bool{}
	for file := range doorExemptions {
		unused[file] = true
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "lycaon"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if skipDoorScanDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if isDoorTestSupport(rel) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		found := durableWriteCalls(fset, rel, file)
		if _, listed := unused[rel]; listed {
			// Exemptions contain a durable write.
			if len(found) > 0 {
				delete(unused, rel)
			}
			return nil
		}
		offences = append(offences, found...)
		return nil
	})
	contractcheck.FailErr(t, "walk the Go tree", err)

	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("durable write outside internal/fseffect — route it through the door, "+
			"or add it to doorExemptions with the property the door cannot express:\n  %s",
			strings.Join(offences, "\n  "))
	}
	if len(unused) > 0 {
		stale := make([]string, 0, len(unused))
		for file := range unused {
			stale = append(stale, file)
		}
		sort.Strings(stale)
		t.Fatalf("doorExemptions lists files that no longer write outside the door "+
			"(delete the line, or the file moved):\n  %s", strings.Join(stale, "\n  "))
	}
}

// durableWriteCalls includes OpenFile only when its flags permit writes.
func durableWriteCalls(fset *token.FileSet, rel string, file *ast.File) []string {
	osName, imported := osImportName(file)
	if !imported {
		return nil
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != osName {
			return true
		}
		name := sel.Sel.Name
		switch {
		case doorPrimitives[name]:
		case name == "OpenFile" && openFileWrites(osName, call):
		default:
			return true
		}
		found = append(found, fmt.Sprintf("%s:%d: os.%s",
			rel, fset.Position(call.Pos()).Line, name))
		return true
	})
	return found
}

func osImportName(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		if spec.Path == nil || spec.Path.Value != `"os"` {
			continue
		}
		if spec.Name == nil {
			return "os", true
		}
		switch spec.Name.Name {
		case "_", ".":
			// A blank import calls nothing; a dot import needs a different scan.
			return "", false
		default:
			return spec.Name.Name, true
		}
	}
	return "", false
}

// Write flags include file creation and truncation without O_WRONLY or O_RDWR.
func openFileWrites(osName string, call *ast.CallExpr) bool {
	if len(call.Args) < 2 {
		return false
	}
	writes := false
	ast.Inspect(call.Args[1], func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != osName {
			return true
		}
		switch sel.Sel.Name {
		case "O_WRONLY", "O_RDWR", "O_CREATE", "O_TRUNC", "O_APPEND":
			writes = true
		}
		return true
	})
	return writes
}

func skipDoorScanDir(name string) bool {
	switch name {
	case "vendor", "node_modules", "testdata", ".git", "dist", "bin":
		return true
	}
	return false
}

// isDoorTestSupport identifies fixture writers whose scratch trees are discarded.
func isDoorTestSupport(rel string) bool {
	return strings.HasPrefix(rel, "lycaon/cmd/codegen-") ||
		strings.HasPrefix(rel, "lycaon/internal/testutil/") ||
		strings.HasPrefix(rel, "lycaon/internal/testbaseline/") ||
		strings.HasPrefix(rel, "lycaon/test/") ||
		strings.HasSuffix(rel, "_testsupport.go")
}
