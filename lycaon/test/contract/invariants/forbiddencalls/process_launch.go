package forbiddencalls

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var processLaunch = []invariants.Invariant{{
	// Process starts carry launch attribution through the typed door in
	// internal/exec; only exec and confine start natively.
	Name:    "process-launches-use-the-typed-door",
	Anchors: []invariants.Anchor{invariants.Func("internal/exec", "PrepareCommand"), invariants.Decl("internal/exec", "ExecOpts.Launch")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal") {
			aliases := importAliases(f.AST)
			prepared := preparedCommandNames(f.AST, aliases)
			emptyErrorReturns := emptyOptionsErrorReturns(f.AST)
			nativeStart := invariants.Under(f, "internal/exec", "internal/confine")
			allowLowLevel := nativeStart || invariants.Under(f, "internal/launchguard")
			ast.Inspect(f.AST, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					if !nativeStart && (isRawProcessStart(node.Fun, aliases) || isRawPreparedStart(node.Fun, prepared)) {
						r.ReportAt(node.Pos(), "raw os/exec process start")
					}
					if !allowLowLevel && isLowLevelProcessStart(node.Fun, aliases) {
						r.ReportAt(node.Pos(), "direct low-level process launch")
					}
					if isLauncherDirectStart(node.Fun, aliases) {
						r.ReportAt(node.Pos(), "direct go-rod launcher start or leakless helper")
					}
				case *ast.CompositeLit:
					if !allowLowLevel && isRawExecCmdLiteral(node.Type, aliases) {
						r.ReportAt(node.Pos(), "raw os/exec.Cmd literal")
					}
					if isLaunchOptionsLiteral(node.Type, f.AST.Name.Name, aliases) && !hasKey(node, "Launch") && !emptyErrorReturns[node] {
						r.ReportAt(node.Pos(), "process options omit Launch")
					}
				}
				return true
			})
		}
	},
}, {
	// Every engine launch entry point routes through the protected broker.
	Name: "engine-launch-doors-route-through-the-broker",
	Anchors: []invariants.Anchor{
		invariants.Func("internal/exec", "relayThroughBroker"),
		invariants.GoPath("internal/exec/pipeline.go"),
		invariants.GoPath("internal/exec/supervisor_linux.go"),
	},
	Check: func(r *invariants.Run) {
		for _, rel := range []string{"internal/exec/pipeline.go", "internal/exec/supervisor_linux.go"} {
			f := r.GoFile(rel)
			routes := false
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				routes = routes || invariants.CalleeName(call) == "relayThroughBroker"
			})
			if !routes {
				r.Report("%s does not route through relayThroughBroker", rel)
			}
		}
	},
}}

func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		out[name] = path
	}
	return out
}

func isRawProcessStart(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && aliases[pkg.Name] == "os/exec"
}

func isLowLevelProcessStart(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch aliases[pkg.Name] {
	case "os":
		return sel.Sel.Name == "StartProcess"
	case "syscall", "golang.org/x/sys/unix":
		return sel.Sel.Name == "ForkExec" || sel.Sel.Name == "StartProcess" || sel.Sel.Name == "Exec"
	default:
		return false
	}
}

func isRawExecCmdLiteral(expr ast.Expr, aliases map[string]string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Cmd" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && aliases[pkg.Name] == "os/exec"
}

func isLauncherDirectStart(expr ast.Expr, aliases map[string]string) bool {
	hasLauncher := false
	for _, path := range aliases {
		if path == "github.com/go-rod/rod/lib/launcher" {
			hasLauncher = true
			break
		}
	}
	if !hasLauncher {
		return false
	}
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && (sel.Sel.Name == "Launch" || sel.Sel.Name == "Leakless")
}

func isLaunchOptionsLiteral(expr ast.Expr, pkg string, aliases map[string]string) bool {
	switch typ := expr.(type) {
	case *ast.Ident:
		return pkg == "exec" && (typ.Name == "ExecOpts" || typ.Name == "PTYOpts")
	case *ast.SelectorExpr:
		name, ok := typ.X.(*ast.Ident)
		if !ok {
			return false
		}
		path := aliases[name.Name]
		return path == "github.com/lycaon/lycaon/internal/exec" && (typ.Sel.Name == "ExecOpts" || typ.Sel.Name == "PTYOpts") ||
			path == "github.com/lycaon/lycaon/internal/hostcmd" && typ.Sel.Name == "Request"
	default:
		return false
	}
}

func hasKey(lit *ast.CompositeLit, want string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == want {
			return true
		}
	}
	return false
}

// Prepared commands keep their actual start algorithm behind the registered
// lifecycle. Native Wait and specialized typed owners remain independent.
func preparedCommandNames(f *ast.File, aliases map[string]string) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Rhs) != 1 || len(assignment.Lhs) == 0 {
			return true
		}
		call, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "PrepareCommand" {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || aliases[pkg.Name] != "github.com/lycaon/lycaon/internal/exec" {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if ok {
			names[name.Name] = true
		}
		return true
	})
	return names
}

func isRawPreparedStart(expr ast.Expr, names map[string]bool) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Start" && selector.Sel.Name != "Run" && selector.Sel.Name != "Output" && selector.Sel.Name != "CombinedOutput") {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && names[receiver.Name]
}

// A zero options result inside an explicit non-nil error branch is not a
// launch request. Successful zero returns and populated values still need Launch.
func emptyOptionsErrorReturns(file *ast.File) map[*ast.CompositeLit]bool {
	out := map[*ast.CompositeLit]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		branch, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		condition, ok := branch.Cond.(*ast.BinaryExpr)
		if !ok || condition.Op != token.NEQ {
			return true
		}
		left, leftOK := condition.X.(*ast.Ident)
		right, rightOK := condition.Y.(*ast.Ident)
		if !leftOK || !rightOK || left.Name != "err" || right.Name != "nil" {
			return true
		}
		for _, statement := range branch.Body.List {
			returned, ok := statement.(*ast.ReturnStmt)
			if !ok || len(returned.Results) != 2 {
				continue
			}
			literal, ok := returned.Results[0].(*ast.CompositeLit)
			if !ok || len(literal.Elts) != 0 {
				continue
			}
			err, ok := returned.Results[1].(*ast.Ident)
			if ok && err.Name == "err" {
				out[literal] = true
			}
		}
		return true
	})
	return out
}
