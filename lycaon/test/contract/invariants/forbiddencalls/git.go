package forbiddencalls

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

var gitDoor = []invariants.Invariant{{
	// Every git subprocess goes through gitexec: no PATH lookup, no fixed
	// binary path, no direct exec, and no scoped config writes.
	Name:    "git-one-door",
	Anchors: []invariants.Anchor{invariants.Package("internal/gitexec")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal", "cmd", "pkg", "config", "test") {
			if invariants.Under(f, "test/contract/invariants") {
				continue
			}
			invariants.StringLits(f.AST, func(lit *ast.BasicLit, value string) {
				if strings.Contains(value, "/usr/bin/git") {
					r.ReportAt(lit.Pos(), "literal /usr/bin/git")
				}
			})
		}
		for _, f := range r.GoFiles(invariants.Production, invariants.Source...) {
			for _, finding := range scanGitOneDoor(f, invariants.Under(f, "internal/gitexec")) {
				r.ReportAt(finding.pos, "%s", finding.what)
			}
		}
	},
}}

type gitFinding struct {
	pos  token.Pos
	what string
}

func scanGitOneDoor(f *invariants.GoFile, inGitexec bool) []gitFinding {
	var out []gitFinding
	completeReads := map[*ast.CompositeLit]bool{}
	f.Funcs(func(fd *ast.FuncDecl) {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ReturnStmt:
				if len(n.Results) == 1 || (len(n.Results) == 2 && isNilIdent(n.Results[1])) {
					if lit, ok := n.Results[0].(*ast.CompositeLit); ok && completeConfigReadArgv(lit) {
						completeReads[lit] = true
					}
				}
			case *ast.CallExpr:
				if isLookPathGit(n) {
					out = append(out, gitFinding{n.Pos(), `LookPath("git")`})
				} else if bin, ok := gitExecBinaryLiteral(n); ok && !inGitexec {
					out = append(out, gitFinding{n.Pos(), "direct exec of " + bin})
				}
			case *ast.CompositeLit:
				if isForbiddenGitConfigArgv(n) && !completeReads[n] {
					out = append(out, gitFinding{n.Pos(), "git config --local/--global/--system write"})
				}
			}
			return true
		})
	})
	return out
}

func isLookPathGit(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "LookPath" || len(call.Args) < 1 {
		return false
	}
	return stringLitIs(call.Args[0], "git") || stringLitIs(call.Args[0], "git-lfs")
}

func gitExecBinaryLiteral(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	name := sel.Sel.Name
	if name != "Command" && name != "CommandContext" && name != "Run" {
		return "", false
	}
	var binArg ast.Expr
	switch name {
	case "CommandContext":
		if len(call.Args) < 2 {
			return "", false
		}
		binArg = call.Args[1]
	case "Run":
		// exec.Run(ctx, name, args, opts) or gitexec.Run(ctx, dir, args, opts)
		if len(call.Args) < 2 {
			return "", false
		}
		// Only flag when the second arg is the binary name string "git".
		binArg = call.Args[1]
	default: // Command
		if len(call.Args) < 1 {
			return "", false
		}
		binArg = call.Args[0]
	}
	if stringLitIs(binArg, "git") {
		return "git", true
	}
	if stringLitIs(binArg, "git-lfs") {
		return "git-lfs", true
	}
	return "", false
}

// isForbiddenGitConfigArgv identifies scope-flagged configuration builders.
func isForbiddenGitConfigArgv(lit *ast.CompositeLit) bool {
	hasConfig := false
	hasScope := false
	for _, elt := range lit.Elts {
		basic, ok := elt.(*ast.BasicLit)
		if !ok || basic.Kind != token.STRING {
			continue
		}
		v, err := strconv.Unquote(basic.Value)
		if err != nil {
			continue
		}
		switch v {
		case "config":
			hasConfig = true
		case "--local", "--global", "--system":
			hasScope = true
		}
	}
	return hasConfig && hasScope
}

func stringLitIs(expr ast.Expr, want string) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	return err == nil && v == want
}

// completeConfigReadArgv recognizes fixed reads with no dynamic option tail.
// Following include directives changes what is read, never what is written.
func completeConfigReadArgv(lit *ast.CompositeLit) bool {
	elts := lit.Elts
	if len(elts) > 2 && stringLitIs(elts[2], "--includes") {
		elts = append(append([]ast.Expr(nil), elts[:2]...), elts[3:]...)
	}
	if len(elts) != 3 && len(elts) != 5 {
		return false
	}
	if !stringLitIs(elts[0], "config") || !stringLitIs(elts[1], "--local") {
		return false
	}
	if len(elts) == 3 {
		return stringLitIs(elts[2], "--list") || stringLitIs(elts[2], "-l")
	}
	if stringLitIs(elts[2], "--list") && stringLitIs(elts[3], "--name-only") && stringLitIs(elts[4], "--null") {
		return true
	}
	return (stringLitIs(elts[2], "--get") || stringLitIs(elts[2], "--get-regexp")) && stringLitIs(elts[3], "--")
}

func isNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

const gitexecPkg = "github.com/lycaon/lycaon/internal/gitexec"

var gitArgv = []invariants.Invariant{{
	// A caller value never lands where git parses options: it follows a
	// flag, a constant prefix, or gitargv.EndOfOptions.
	Name:    "git-argv-keeps-caller-values-out-of-option-positions",
	Anchors: []invariants.Anchor{invariants.Func("internal/gitexec", "Run"), invariants.Decl("internal/gitargv", "EndOfOptions")},
	Check: func(r *invariants.Run) {
		checked := 0
		for _, f := range r.GoFiles(func(f *invariants.GoFile) bool { return !f.IsTest && f.Dir == "internal/git" }, "internal/git") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg != gitexecPkg || name != "Run" || len(call.Args) < 3 {
					return
				}
				lit, ok := call.Args[2].(*ast.CompositeLit)
				if !ok {
					return
				}
				checked++
				terminated := false
				for i, elt := range lit.Elts {
					if isArgvTerminator(f, elt) {
						terminated = true
						continue
					}
					if argvConstant(elt) || terminated || leftmostIsStringConstant(elt) || i > 0 && argvFlagLiteral(lit.Elts[i-1]) {
						continue
					}
					r.ReportAt(elt.Pos(), "caller value in an option position; insert gitargv.EndOfOptions before it")
				}
			})
		}
		r.Expect(checked, "inline gitexec.Run argv in internal/git")
	},
}}

// isArgvTerminator reports gitargv.EndOfOptions or a literal "--".
func isArgvTerminator(f *invariants.GoFile, e ast.Expr) bool {
	if value, ok := invariants.StringLit(e); ok {
		return value == "--"
	}
	pkg, name, ok := f.Qualified(e)
	return ok && pkg == "github.com/lycaon/lycaon/internal/gitargv" && name == "EndOfOptions"
}

// argvConstant reports a plain string constant element.
func argvConstant(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING
}

func argvFlagLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	return strings.HasPrefix(strings.Trim(lit.Value, `"`), "-")
}

func leftmostIsStringConstant(e ast.Expr) bool {
	for {
		bin, ok := e.(*ast.BinaryExpr)
		if !ok || bin.Op != token.ADD {
			return argvConstant(e)
		}
		e = bin.X
	}
}
