package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// confinementProducers name every file that builds a confinement, with the
// provenance of its roots.
var confinementProducers = []laws.Exemption{
	{Key: "internal/tools/native/command/program_external.go", Reason: "programEnvironment: DefaultConfinement from ActionConfineInputsForContext; host-derived roots and grants for the shared launch environment"},
	{Key: "internal/tools/native/command/command_exec.go", Reason: "commandConfineRoots(tctx, extraWriteRoots): host-selected root, attached roots and the approved chat write-root overlay"},
	{Key: "internal/tools/native/command/background_tools.go", Reason: "commandConfineRoots(tctx, extraWriteRoots): the same union as a foreground command"},
	{Key: "internal/tools/native/terminal/session_open.go", Reason: "tools.ConfineRootsForAction(tctx): host provenance only"},
	{Key: "internal/tools/safecmd/safecmd.go", Reason: "Confine(roots) wrapper; callers supply session roots"},
	{Key: "internal/mcp/connector.go", Reason: "stdioConfinement(roots): server spawn roots from the session, never from tool arguments"},
	{Key: "internal/scan/bundled_confinement.go", Reason: "bundled OpenGrep: project read-only plus the exact host output root, network denied, mandatory application"},
	{Key: "internal/confine/confine.go", Reason: "DefaultConfinement and the profile builders themselves"},
	{Key: "internal/hitl/contained.go", Reason: "ContainedForRequest projection: reads the same union the executor applies"},
	{Key: "internal/toolexecution/command_operand_expansion.go", Reason: "globReadable(confReq): read boundaries before a grant, for glob expansion"},
	{Key: "internal/documentcore/process.go", Reason: "native document core: only its own scratch root, network denied, mandatory application"},
	{Key: "internal/visualscreen/ocr_tesseract.go", Reason: "bundled OCR engine: only its own per-image scratch root, network denied, mandatory application"},
	{Key: "internal/layeredfs/adapter/viewconfine/viewconfine.go", Reason: "workerBox(view roots): a worker's shape, to prove denial; host-derived roots only, network denied"},
	{Key: "internal/exec/prepared.go", Reason: "Confinement{HostExecution: true}: no roots; the explicit host disposition when a launch plan carries no agent boundary"},
}

var confinement = []laws.Rule{{
	// Execution boundaries consult one refusal predicate, and durable roots
	// never come from a string literal.
	Name:    "write-root-producers-share-one-predicate",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "AttachedWriteRootRefused"), laws.Decl("internal/confine", "Confinement.Roots")},
	Check: func(r *laws.Run) {
		consulted := false
		for _, f := range r.GoFiles(laws.Production, "internal") {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.FuncDecl:
					if node.Name.Name == "AttachedWriteRootRefused" && node.Recv == nil && laws.Under(f, "internal/confine") {
						return false
					}
				case *ast.Ident:
					consulted = consulted || node.Name == "AttachedWriteRootRefused" && laws.Under(f, "internal/confine")
				case *ast.CompositeLit:
					if !laws.Under(f, "internal/confine") && laws.TypeName(node.Type) == "Confinement" && literalRoots(node) {
						r.ReportAt(node.Pos(), "Confinement.Roots built from a string literal; roots come through AttachedWriteRootRefused gates")
					}
				}
				return true
			})
		}
		if !consulted {
			r.Report("internal/confine never consults AttachedWriteRootRefused")
		}
	},
}, {
	// Every file that builds a confinement is reviewed with its roots'
	// provenance; new producers route roots through tools.ConfineRootsForAction.
	Name:    "confinement-producers-are-inventoried",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "DefaultConfinement"), laws.Func("internal/tools", "ConfineRootsForAction")},
	Exempt:  confinementProducers,
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			produces := false
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					switch laws.CalleeName(node) {
					case "DefaultConfinement", "stdioConfinement", "Confine":
						produces = true
					}
				case *ast.CompositeLit:
					produces = produces || laws.TypeName(node.Type) == "Confinement"
				}
				return !produces
			})
			if produces && !r.Exempt(f.Rel) {
				r.Report("%s builds a confinement outside the reviewed producers", f.Rel)
			}
		}
	},
}, {
	// Confinement roots come from path facts, not ecosystem cache layouts.
	Name:    "confine-names-no-ecosystem-caches",
	Anchors: []laws.Anchor{laws.Package("internal/confine")},
	Exempt:  []laws.Exemption{{Key: "internal/confine/package_cache_roots.go", Reason: "the bounded package-store exception, held clear of executable install directories by the security contract"}},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/confine") {
			laws.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				for _, needle := range []string{"/pkg/mod/", ".npm", ".cargo", "GOPATH", "GOCACHE"} {
					if strings.Contains(value, needle) && !r.Exempt(f.Rel) {
						r.ReportAt(lit.Pos(), "names the ecosystem cache %s", needle)
					}
				}
			})
		}
	},
}}

func literalRoots(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Roots" {
			continue
		}
		if slice, ok := kv.Value.(*ast.CompositeLit); ok {
			for _, e := range slice.Elts {
				if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					return true
				}
			}
		}
	}
	return false
}
