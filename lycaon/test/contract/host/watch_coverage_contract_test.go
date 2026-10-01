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

// Epoch equality is authoritative only within matching watch coverage.
// Exempt comparisons do not use equality as a freshness signal.
var epochTrustExemptions = map[string]string{
	"lycaon/internal/editordoc/service.go:projectionVector":                 "compares retained CRDT replica identity to select a differential update; source freshness is established by the observed document open",
	"lycaon/internal/editordoc/rewind_semantic.go:semanticRewindUndos":      "compares retained CRDT identity epochs",
	"lycaon/internal/sourceledger/recorded_text_edits.go:recordedTextEdits": "compares CRDT identity epochs on immutable retained versions, not filesystem observation freshness",
	"lycaon/internal/upgradefixture/editor_history.go:verifyEditorCore":     "verifies retained CRDT replica head epoch in upgrade rehearsal fixture against recorded evidence",
	"lycaon/internal/repochange/notify.go:EpochCurrent":                     "the primitive itself; coverage is a separate question its callers ask",

	"lycaon/internal/sourcecatalog/refresh.go:buildRoot": "the comparison rejects a catalog build whose tree moved under it; it reuses nothing, " +
		"and any consumer of the stamped generation is the one that must trust it",
	"lycaon/internal/sourcecatalog/store_core.go:settleEpoch": "records the epoch a completed pass observed and queues known concurrent changes; " +
		"openStore owns reuse and consults coverage with a bounded validation age",

	"lycaon/internal/sourceledger/inventory.go:InventoryState": "reports whether the completed inventory matches the requested one; the decision to " +
		"refresh belongs to EnsureInventory, which consults coverage",
}

func TestEpochFastPathsConsultWatchCoverage(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	funcs, err := epochAwareFuncs(root)
	contractcheck.FailErr(t, "scan for epoch comparisons", err)

	consults := coverageConsultingFuncs(funcs)
	var offences []string
	unused := map[string]bool{}
	for key := range epochTrustExemptions {
		unused[key] = true
	}
	for _, fn := range funcs {
		if !fn.comparesEpochs {
			continue
		}
		if _, listed := unused[fn.key]; listed {
			delete(unused, fn.key)
			continue
		}
		if consults[fn] {
			continue
		}
		offences = append(offences, fn.key)
	}
	if len(offences) > 0 {
		sort.Strings(offences)
		t.Fatalf("epoch equality trusted without repochange.Coverage or repochange.DirWatched:\n  %s\n"+
			"A truncated watch stops the epoch moving without reporting anything, so equality proves "+
			"nothing there. Consult coverage at the grain of the claim — DirWatched for one directory, "+
			"Coverage for the tree — and re-read where it comes back false, or list the function with "+
			"the reason its comparison cannot skip work.", strings.Join(offences, "\n  "))
	}
	if len(unused) > 0 {
		stale := make([]string, 0, len(unused))
		for key := range unused {
			stale = append(stale, key)
		}
		sort.Strings(stale)
		t.Fatalf("listed functions no longer compare epochs (delete the line, or they moved):\n  %s",
			strings.Join(stale, "\n  "))
	}
}

type epochFunc struct {
	key            string // repo-relative file:FuncName
	name           string // function name alone, for call resolution
	pkg            string // repo-relative package directory
	comparesEpochs bool
	consults       bool
	calls          []string
}

func epochAwareFuncs(root string) ([]*epochFunc, error) {
	var out []*epochFunc
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
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			out = append(out, inspectEpochFunc(rel, fn))
		}
		return nil
	})
	return out, err
}

func inspectEpochFunc(rel string, fn *ast.FuncDecl) *epochFunc {
	info := &epochFunc{key: fmt.Sprintf("%s:%s", rel, fn.Name.Name), name: fn.Name.Name,
		pkg: filepath.ToSlash(filepath.Dir(rel))}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if (node.Op == token.EQL || node.Op == token.NEQ) &&
				!isLiteralOperand(node.X) && !isLiteralOperand(node.Y) &&
				(namesEpoch(node.X) || namesEpoch(node.Y)) {
				info.comparesEpochs = true
			}
		case *ast.CallExpr:
			name, pkg := epochCallName(node)
			switch {
			case pkg == "repochange" && name == "EpochCurrent":
				info.comparesEpochs = true
			case pkg == "repochange" && (name == "Coverage" || name == "DirWatched"):
				info.consults = true
			case name != "":
				info.calls = append(info.calls, name)
			}
		}
		return true
	})
	return info
}

// coverageConsultingFuncs follows local calls and unambiguous cross-package names to Coverage.
func coverageConsultingFuncs(funcs []*epochFunc) map[*epochFunc]bool {
	byPkg := map[string]*epochFunc{}
	byName := map[string][]*epochFunc{}
	for _, fn := range funcs {
		byPkg[fn.pkg+"."+fn.name] = fn
		byName[fn.name] = append(byName[fn.name], fn)
	}
	resolve := func(from *epochFunc, callee string) *epochFunc {
		if target, ok := byPkg[from.pkg+"."+callee]; ok {
			return target
		}
		if candidates := byName[callee]; len(candidates) == 1 {
			return candidates[0]
		}
		return nil
	}
	consults := map[*epochFunc]bool{}
	for _, fn := range funcs {
		if fn.consults {
			consults[fn] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range funcs {
			if consults[fn] {
				continue
			}
			for _, callee := range fn.calls {
				if target := resolve(fn, callee); target != nil && consults[target] {
					consults[fn] = true
					changed = true
					break
				}
			}
		}
	}
	return consults
}

func epochCallName(call *ast.CallExpr) (name, pkg string) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name, ""
	case *ast.SelectorExpr:
		if id, ok := fun.X.(*ast.Ident); ok {
			return fun.Sel.Name, id.Name
		}
		return fun.Sel.Name, ""
	}
	return "", ""
}

func isLiteralOperand(expr ast.Expr) bool {
	_, ok := expr.(*ast.BasicLit)
	return ok
}

// namesEpoch ignores package qualifiers when identifying epoch operands.
func namesEpoch(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if strings.Contains(strings.ToLower(node.Sel.Name), "epoch") {
				found = true
				return false
			}
			if _, isPkg := node.X.(*ast.Ident); isPkg {
				return false
			}
		case *ast.Ident:
			if strings.Contains(strings.ToLower(node.Name), "epoch") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
