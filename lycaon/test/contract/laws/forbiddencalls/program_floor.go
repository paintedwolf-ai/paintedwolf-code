package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var programFloor = []laws.Rule{{
	// Settings hold no command-name permission floor; the floor is
	// confinement facts, never a list of program names.
	Name:    "no-program-name-floor-under-settings",
	Anchors: []laws.Anchor{laws.Package("internal/settings")},
	Check:   func(r *laws.Run) { checkProgramNameFloor(r, "internal/settings") },
}, {
	// Confinement and project code hold no command-name permission floor.
	Name:    "no-program-name-floor-under-confine-and-project",
	Anchors: []laws.Anchor{laws.Package("internal/confine"), laws.Package("internal/project")},
	Check:   func(r *laws.Run) { checkProgramNameFloor(r, "internal/confine", "internal/project") },
}}

func checkProgramNameFloor(r *laws.Run, dirs ...string) {
	for _, f := range r.GoFiles(laws.Production, dirs...) {
		locals := localSingleAssignments(f.AST)
		ast.Inspect(f.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				if isProgramNameBoolMap(node) {
					r.ReportAt(node.Pos(), "a map[string]bool of program names is a command-name floor")
				}
			case *ast.SwitchStmt:
				if switchOnFilepathBase(node.Tag, locals) && switchHasProgramNameCases(node) {
					r.ReportAt(node.Pos(), "switches on a program name from filepath.Base")
				}
			}
			return true
		})
	}
}

func isProgramNameBoolMap(expr ast.Expr) bool {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	mt, ok := cl.Type.(*ast.MapType)
	if !ok {
		return false
	}
	key, ok := mt.Key.(*ast.Ident)
	if !ok || key.Name != "string" {
		return false
	}
	val, ok := mt.Value.(*ast.Ident)
	if !ok || val.Name != "bool" {
		return false
	}
	if len(cl.Elts) == 0 {
		return false
	}
	programish := 0
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		lit, ok := kv.Key.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			continue
		}
		if looksLikeProgramName(s) {
			programish++
		}
	}
	return programish >= 2
}

// localSingleAssignments maps an identifier to the expression assigned to it,
// for identifiers assigned exactly once in the file. `base := filepath.Base(p)`
// then `switch base` is the same floor as switching on the call. A name assigned
// more than once is dropped rather than guessed at.
func localSingleAssignments(f *ast.File) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	shadowed := map[string]bool{}
	record := func(lhs []ast.Expr, rhs []ast.Expr) {
		if len(lhs) != 1 || len(rhs) != 1 {
			return
		}
		ident, ok := lhs[0].(*ast.Ident)
		if !ok || ident.Name == "_" {
			return
		}
		if _, seen := out[ident.Name]; seen {
			shadowed[ident.Name] = true
			return
		}
		out[ident.Name] = rhs[0]
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			record(node.Lhs, node.Rhs)
		case *ast.ValueSpec:
			lhs := make([]ast.Expr, 0, len(node.Names))
			for _, name := range node.Names {
				lhs = append(lhs, name)
			}
			record(lhs, node.Values)
		}
		return true
	})
	for name := range shadowed {
		delete(out, name)
	}
	return out
}

// switchOnFilepathBase reports whether the switch tag is a filepath.Base result,
// directly or through one local assignment, and through one wrapping call
// (`strings.ToLower(filepath.Base(p))` is the same tag).
func switchOnFilepathBase(tag ast.Expr, locals map[string]ast.Expr) bool {
	if tag == nil {
		return false
	}
	if ident, ok := tag.(*ast.Ident); ok {
		assigned, found := locals[ident.Name]
		if !found {
			return false
		}
		// One hop only: locals carries no scope, so a chain would guess across
		// functions.
		return isFilepathBaseCall(assigned)
	}
	return isFilepathBaseCall(tag)
}

func isFilepathBaseCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "filepath" && sel.Sel.Name == "Base" {
		return true
	}
	// A wrapper — strings.ToLower, strings.TrimSpace — leaves the tag a program name.
	for _, arg := range call.Args {
		if isFilepathBaseCall(arg) {
			return true
		}
	}
	return false
}

func switchHasProgramNameCases(sw *ast.SwitchStmt) bool {
	count := 0
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range cc.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			if looksLikeProgramName(s) {
				count++
			}
		}
	}
	return count >= 1
}

func looksLikeProgramName(s string) bool {
	if s == "" || strings.ContainsAny(s, "/\\ ") {
		return false
	}
	if strings.Contains(s, ".") && !strings.HasPrefix(s, "mkfs") {
		// Allow dotted tool ids (mcp.foo) — not argv[0] floor entries.
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' && r != '*' {
			return false
		}
	}
	return true
}
