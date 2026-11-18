package contract

import (
	"go/ast"
	"go/token"
	"strconv"
)

func scanGitOneDoor(fset *token.FileSet, file *ast.File, rel string, inGitexec bool) []string {
	var out []string
	completeReads := map[*ast.CompositeLit]bool{}
	scanCall := func(n *ast.CallExpr) {
		if isLookPathGit(n) {
			out = append(out, fset.Position(n.Pos()).String()+`: LookPath("git")`)
			return
		}
		if !inGitexec {
			if bin, ok := gitExecBinaryLiteral(n); ok {
				out = append(out, fset.Position(n.Pos()).String()+`: direct exec of `+bin)
			}
		}
	}
	scanLit := func(n *ast.CompositeLit) {
		if isForbiddenGitConfigArgv(n) && !completeReads[n] {
			out = append(out, fset.Position(n.Pos()).String()+`: git config --local/--global/--system write`)
		}
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ReturnStmt:
				if len(n.Results) == 1 || (len(n.Results) == 2 && isNilIdent(n.Results[1])) {
					if lit, ok := n.Results[0].(*ast.CompositeLit); ok && completeConfigReadArgv(lit) {
						completeReads[lit] = true
					}
				}
			case *ast.CallExpr:
				scanCall(n)
			case *ast.CompositeLit:
				scanLit(n)
			}
			return true
		})
	}
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
