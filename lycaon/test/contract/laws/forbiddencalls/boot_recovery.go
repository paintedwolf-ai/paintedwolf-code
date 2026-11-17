package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

const bootRecoveryPkg = "github.com/lycaon/lycaon/internal/bootrecovery"

var bootRecovery = []laws.Rule{{
	// Startup recovery registers a bootrecovery.Entry: ordering and failure
	// policy belong to the registry.
	Name:    "startup-recovery-runs-through-the-registry",
	Anchors: []laws.Anchor{laws.Decl("internal/bootrecovery", "Entry")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/app") {
			var stack []ast.Node
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				if call, ok := n.(*ast.CallExpr); ok {
					name := laws.CalleeName(call)
					if (strings.HasPrefix(name, "Recover") || strings.HasPrefix(name, "runRecover")) &&
						!insideEntry(f, stack) {
						r.ReportAt(call.Pos(), "calls %s inline; register a bootrecovery.Entry", name)
					}
				}
				stack = append(stack, n)
				return true
			})
		}
	},
}, {
	// An entry that omits a field cannot be registered for startup.
	Name:    "recovery-entries-declare-kind-and-phase",
	Anchors: []laws.Anchor{laws.Decl("internal/bootrecovery", "Entry.Kind"), laws.Decl("internal/bootrecovery", "Entry.Phase")},
	Check: func(r *laws.Run) {
		entries := 0
		for _, f := range r.GoFiles(laws.Production, "internal/app") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isRecoveryEntry(f, lit) {
					return true
				}
				entries++
				for _, field := range []string{"Name", "Kind", "Phase", "Run"} {
					if !setsField(lit, field) {
						r.ReportAt(lit.Pos(), "bootrecovery.Entry omits %s", field)
					}
				}
				return true
			})
		}
		r.Expect(entries, "bootrecovery.Entry literals in internal/app")
	},
}}

func isRecoveryEntry(f *laws.GoFile, lit *ast.CompositeLit) bool {
	pkg, name, ok := f.Qualified(lit.Type)
	return ok && pkg == bootRecoveryPkg && name == "Entry"
}

func insideEntry(f *laws.GoFile, stack []ast.Node) bool {
	for _, n := range stack {
		if lit, ok := n.(*ast.CompositeLit); ok && isRecoveryEntry(f, lit) {
			return true
		}
	}
	return false
}
