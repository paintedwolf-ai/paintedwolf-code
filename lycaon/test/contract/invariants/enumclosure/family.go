// Package enumclosure holds the invariants that close a declared vocabulary over
// its uses: every value is registered where the host enumerates it and
// emitted where the host produces it.
package enumclosure

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// Family is the enum-closure rule table.
var Family = invariants.Family{Name: "enum-closure", Invariants: []invariants.Invariant{
	{Name: "extension-diagnostics-are-registered-and-graded", Anchors: []invariants.Anchor{invariants.Decl("internal/extpacks", "allDiagnosticCodes"), invariants.Decl("internal/extpacks", "diagnosticSeverity")}, Check: extensionDiagnosticsRegisteredAndGraded},
	{Name: "worker-promote-outcomes-are-emitted", Anchors: []invariants.Anchor{invariants.Decl("pkg/api", "WorkerPromotePathOutcome")}, Check: workerPromoteOutcomesAreEmitted},
	{Name: "worker-promote-reasons-are-referenced", Anchors: []invariants.Anchor{invariants.Package("pkg/api")}, Check: workerPromoteReasonsAreReferenced},
}}

// consts returns the constants of a package that keep selects, by name.
func consts(r *invariants.Run, dir string, keep func(*types.Const) bool) map[string]*types.Const {
	scope := r.TypedPackage(dir).Types.Scope()
	out := map[string]*types.Const{}
	for _, name := range scope.Names() {
		if c, ok := scope.Lookup(name).(*types.Const); ok && keep(c) {
			out[name] = c
		}
	}
	r.Expect(len(out), "constants in "+dir)
	return out
}

// referenced reports the constants of want that production code below dirs
// refers to.
func referenced(r *invariants.Run, want map[string]*types.Const, dirs ...string) map[string]bool {
	used := map[string]bool{}
	for _, pkg := range r.TypedPackages(dirs...) {
		for _, obj := range pkg.Info.Uses {
			if c, ok := obj.(*types.Const); ok && want[c.Name()] == c {
				used[c.Name()] = true
			}
		}
	}
	return used
}

// Each Diag* constant is listed in allDiagnosticCodes and graded in
// diagnosticSeverity, or it silently defaults to error; the registry lists
// only declared constants.
func extensionDiagnosticsRegisteredAndGraded(r *invariants.Run) {
	declared := consts(r, "internal/extpacks", func(c *types.Const) bool { return strings.HasPrefix(c.Name(), "Diag") })
	registered := identsIn(r.Declaration("internal/extpacks", "allDiagnosticCodes").Node, false)
	graded := identsIn(r.Declaration("internal/extpacks", "diagnosticSeverity").Node, true)
	r.Expect(len(registered), "allDiagnosticCodes entries")
	r.Expect(len(graded), "diagnosticSeverity rows")
	for _, name := range sortedKeys(declared) {
		if !slices.Contains(registered, name) {
			r.Report("%s is declared but missing from allDiagnosticCodes", name)
		}
		if !slices.Contains(graded, name) {
			r.Report("%s has no diagnosticSeverity row, so it defaults to error", name)
		}
	}
	for _, name := range registered {
		if declared[name] == nil {
			r.Report("allDiagnosticCodes lists %s, which is not a declared diagnostic constant", name)
		}
	}
}

// identsIn lists the identifier elements of a composite literal, or its
// identifier keys.
func identsIn(node ast.Node, keys bool) []string {
	var out []string
	ast.Inspect(node, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok && keys {
				elt = kv.Key
			}
			if id, ok := elt.(*ast.Ident); ok {
				out = append(out, id.Name)
			}
		}
		return false
	})
	return out
}

// Every WorkerPromotePathOutcome value is produced by the worker package.
func workerPromoteOutcomesAreEmitted(r *invariants.Run) {
	declared := consts(r, "pkg/api", func(c *types.Const) bool {
		named, ok := c.Type().(*types.Named)
		return ok && named.Obj().Name() == "WorkerPromotePathOutcome"
	})
	used := referenced(r, declared, "internal/worker")
	for _, name := range sortedKeys(declared) {
		if !used[name] {
			r.Report("api.%s is never produced in internal/worker; emit it from a production path or remove it", name)
		}
	}
}

// Every WorkerPromoteReason code is referenced by worker or session code.
func workerPromoteReasonsAreReferenced(r *invariants.Run) {
	declared := consts(r, "pkg/api", func(c *types.Const) bool {
		return strings.HasPrefix(c.Name(), "WorkerPromoteReason")
	})
	used := referenced(r, declared, "internal/worker", "internal/session")
	for _, name := range sortedKeys(declared) {
		if !used[name] {
			r.Report("api.%s is never referenced in worker or session production code", name)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
