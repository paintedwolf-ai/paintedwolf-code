package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var authorities = []laws.Rule{{
	// Loopback decided from a host string has one authority, internal/egress.
	// Deciding it from a resolved netip.Addr is the floor and needs none.
	Name:    "loopback-from-a-name-has-one-authority",
	Anchors: []laws.Anchor{laws.Func("internal/egress", "SyntacticLoopback"), laws.Func("internal/egress", "LoopbackLiteral")},
	Exempt: []laws.Exemption{
		{Key: "internal/hostproxy/policy.go", Reason: "proxy bypass also treats the RFC 6761 *.localhost names as local, which suits skipping a proxy and not deciding what to bind"},
		{Key: "internal/projectstack/doclinks.go", Reason: "asks whether a documentation URL is public, covering private, unspecified and link-local ranges too"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/egress") || !fileDecidesLoopbackFromAString(f.AST) || r.Exempt(f.Rel) {
				continue
			}
			r.Report("%s decides loopback from a host string: use egress.SyntacticLoopback for a name, egress.LoopbackLiteral for an address literal", f.Rel)
		}
	},
}, {
	// A file that consults a classification authority resolves paths through
	// fspath.CanonicalPath: EvalSymlinks cannot see the /System/Volumes/Data
	// firmlink or a case-insensitive volume, so one location keeps two names.
	Name:    "path-identity-has-one-authority",
	Anchors: []laws.Anchor{laws.Func("internal/fspath", "CanonicalPath")},
	Exempt: []laws.Exemption{
		{Key: "internal/projectsource/source_lifecycle.go", Reason: "lifecycle writes reject dangling parent links and retain the selected directory entry"},
		{Key: "internal/fseffect/transaction_windows.go", Reason: "atomic replace refuses symlink components rather than following them"},
	},
	Check: func(r *laws.Run) {
		classifiers := []string{
			"github.com/lycaon/lycaon/internal/sensitivepath",
			"github.com/lycaon/lycaon/internal/settingsoverlay",
			"github.com/lycaon/lycaon/internal/grantedpath",
		}
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/fspath") || !callsFilepath(f, f.AST, "EvalSymlinks") || r.Exempt(f.Rel) {
				continue
			}
			for _, p := range classifiers {
				if f.Imports(p) {
					r.Report("%s consults %s and resolves paths itself; use fspath.CanonicalPath", f.Rel, p)
					break
				}
			}
			if resolvesByClimbing(f) {
				r.Report("%s resolves a missing path tail by climbing parents; use fspath.CanonicalPath", f.Rel)
			}
		}
	},
}}

// callsFilepath reports a path/filepath.name call below node.
func callsFilepath(f *laws.GoFile, node ast.Node, name string) bool {
	found := false
	f.Calls(node, func(_ *ast.CallExpr, pkg, fn string) {
		found = found || pkg == "path/filepath" && fn == name
	})
	return found
}

// resolvesByClimbing finds a loop that both resolves symlinks and takes
// filepath.Dir.
func resolvesByClimbing(f *laws.GoFile) bool {
	found := false
	ast.Inspect(f.AST, func(n ast.Node) bool {
		var body ast.Node
		switch loop := n.(type) {
		case *ast.ForStmt:
			body = loop.Body
		case *ast.RangeStmt:
			body = loop.Body
		default:
			return !found
		}
		found = found || callsFilepath(f, body, "EvalSymlinks") && callsFilepath(f, body, "Dir")
		return !found
	})
	return found
}

// fileDecidesLoopbackFromAString finds predicates that infer loopback from a name.
// Predicates over resolved addresses are outside this rule.
func fileDecidesLoopbackFromAString(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !takesAName(fn) || !returnsBool(fn) {
			return true
		}
		if bodyDecidesLoopback(fn.Body) {
			found = true
			return false
		}
		return true
	})
	return found
}

// takesAName reports a parameter that is a spelling rather than a resolved
// address: a bare string, or a URL, which is a string with structure.
func takesAName(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		switch t := field.Type.(type) {
		case *ast.Ident:
			if t.Name == "string" {
				return true
			}
		case *ast.StarExpr:
			if sel, ok := t.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "URL" {
				return true
			}
		}
	}
	return false
}

func returnsBool(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "bool" {
			return true
		}
	}
	return false
}

// bodyDecidesLoopback looks for the reserved name used in a comparison, or a
// parse-then-IsLoopback pair. A bare mention of the word is not enough.
func bodyDecidesLoopback(body *ast.BlockStmt) bool {
	comparesName, parses, checksLoopback := false, false, false
	isLocalhost := func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		return ok && strings.EqualFold(strings.Trim(lit.Value, `\"`), "localhost")
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op.String() == "==" && (isLocalhost(node.X) || isLocalhost(node.Y)) {
				comparesName = true
			}
		case *ast.CaseClause:
			for _, expr := range node.List {
				if isLocalhost(expr) {
					comparesName = true
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "ParseIP", "ParseAddr":
					parses = true
				case "IsLoopback":
					checksLoopback = true
				case "EqualFold":
					for _, arg := range node.Args {
						if isLocalhost(arg) {
							comparesName = true
						}
					}
				}
			}
		}
		return true
	})
	return comparesName || (parses && checksLoopback)
}

// confinementProducers name every file that builds a confinement, with the
// provenance of its roots.
var confinementProducers = []laws.Exemption{
	{Key: "internal/tools/native/command/program_external.go", Reason: "programEnvironment: DefaultConfinement from ActionConfineInputsForContext; host-derived roots and grants for the shared launch environment"},
	{Key: "internal/tools/native/command/command_exec.go", Reason: "commandConfineRoots(tctx, extraWriteRoots): host-selected root, attached roots and the approved chat write-root overlay"},
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
