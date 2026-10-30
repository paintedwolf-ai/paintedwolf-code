package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestGitOneDoorContract proves every git subprocess in lycaon/internal goes
// through gitexec: no PATH LookPath("git"), no /usr/bin/git literal, no direct
// exec of "git"/"git-lfs", and no config --local/--global/--system writes.
// Every non-test Go tree that ships in the sidecar is scanned, including cmd/
// and pkg/.
func TestGitOneDoorContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	fset := token.NewFileSet()
	var findings []string

	for _, tree := range []string{"internal", "cmd", "pkg"} {
		treeRoot := filepath.Join(root, "lycaon", tree)
		if _, statErr := os.Stat(treeRoot); statErr != nil {
			continue
		}
		err := filepath.WalkDir(treeRoot, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "/usr/bin/git") {
				findings = append(findings, rel+`: literal /usr/bin/git`)
			}
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return err
			}
			inGitexec := strings.Contains(filepath.ToSlash(rel), "/internal/gitexec/")
			findings = append(findings, scanGitOneDoor(fset, file, rel, inGitexec)...)
			return nil
		})
		contractcheck.FailErr(t, "walk "+tree, err)
	}

	if len(findings) > 0 {
		t.Fatalf("git one-door violations:\n  %s", strings.Join(findings, "\n  "))
	}
}

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
func completeConfigReadArgv(lit *ast.CompositeLit) bool {
	if len(lit.Elts) != 3 && len(lit.Elts) != 5 {
		return false
	}
	if !stringLitIs(lit.Elts[0], "config") || !stringLitIs(lit.Elts[1], "--local") {
		return false
	}
	if len(lit.Elts) == 3 {
		return stringLitIs(lit.Elts[2], "--list") || stringLitIs(lit.Elts[2], "-l")
	}
	if stringLitIs(lit.Elts[2], "--list") && stringLitIs(lit.Elts[3], "--name-only") && stringLitIs(lit.Elts[4], "--null") {
		return true
	}
	return (stringLitIs(lit.Elts[2], "--get") || stringLitIs(lit.Elts[2], "--get-regexp")) && stringLitIs(lit.Elts[3], "--")
}

func isNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}
