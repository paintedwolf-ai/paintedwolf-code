// Command testsleep reports fixed real-clock sleeps in Go tests.
//
// A time.Sleep that is a statement of its own, outside any loop, waits a fixed
// span for something it does not observe. Such a test passes on a host that
// is fast enough and fails or proves nothing on one that is not. A sleep is
// allowed inside a loop (a poll interval), inside a testing/synctest bubble
// (virtual time), and in a helper process that a parent test starts and
// kills: TestMain, a branch the process environment selects, a function that
// ends the process, or one whose result is the process's exit status.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	root := flag.String("root", ".", "directory to scan")
	flag.Parse()
	findings, err := scan(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testsleep:", err)
		os.Exit(2)
	}
	for _, finding := range findings {
		fmt.Println(finding)
	}
	if len(findings) > 0 {
		fmt.Printf("%d fixed sleep(s) in tests: wait on the event, a registered testutil wait, or synctest\n", len(findings))
		os.Exit(1)
	}
}

func scan(root string) ([]string, error) {
	var findings []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if path != root && (name == "testdata" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		found, err := sleepsIn(path)
		findings = append(findings, found...)
		return err
	})
	sort.Strings(findings)
	return findings, err
}

func sleepsIn(path string) ([]string, error) {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := importNames(file)
	if names["time"] == "" {
		return nil, nil
	}
	exits := exitStatusFunctions(file, names["os"])
	var findings []string
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, node)
		statement, ok := node.(*ast.ExprStmt)
		if !ok || !isCall(statement.X, names["time"], "Sleep") || allowed(stack, names, exits) {
			return true
		}
		position := files.Position(statement.Pos())
		findings = append(findings, fmt.Sprintf("%s:%d: fixed sleep", filepath.ToSlash(position.Filename), position.Line))
		return true
	})
	return findings, nil
}

// importNames maps each import path this check cares about to its local name.
func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		switch path {
		case "time", "testing/synctest", "os":
			names[path] = name
		}
	}
	return names
}

// allowed walks out from the sleep to the nearest function: a loop, a synctest
// bubble or a helper-process branch on the way makes the sleep legitimate. A
// loop counts only within the sleep's own function; a bubble encloses every
// closure it runs.
func allowed(stack []ast.Node, names map[string]string, exits map[string]bool) bool {
	os := names["os"]
	inClosure := false
	for i := len(stack) - 2; i >= 0; i-- {
		switch node := stack[i].(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			if !inClosure {
				return true
			}
		case *ast.IfStmt:
			if readsEnvironment(node.Init, os) || readsEnvironment(node.Cond, os) {
				return true
			}
		case *ast.SwitchStmt:
			if readsEnvironment(node.Init, os) || readsEnvironment(node.Tag, os) {
				return true
			}
		case *ast.FuncLit:
			if i > 0 && isBubble(stack[i-1], names["testing/synctest"]) {
				return true
			}
			inClosure = true
		case *ast.FuncDecl:
			return node.Name.Name == "TestMain" || exits[node.Name.Name] || endsProcess(node.Body, os) ||
				gatedByEnvironment(node.Body, stack[len(stack)-1].Pos(), os)
		}
	}
	return false
}

// exitStatusFunctions names the functions whose result this file passes to os.Exit.
func exitStatusFunctions(file *ast.File, os string) map[string]bool {
	names := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isCall(call, os, "Exit") || len(call.Args) != 1 {
			return true
		}
		if inner, ok := call.Args[0].(*ast.CallExpr); ok {
			if ident, ok := inner.Fun.(*ast.Ident); ok {
				names[ident.Name] = true
			}
		}
		return true
	})
	return names
}

func endsProcess(body *ast.BlockStmt, os string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if call, ok := node.(ast.Expr); ok && isCall(call, os, "Exit") {
			found = true
		}
		return !found
	})
	return found
}

// gatedByEnvironment reports a top-level environment check before pos that leaves
// the function, the usual entry of a helper-process test.
func gatedByEnvironment(body *ast.BlockStmt, pos token.Pos, os string) bool {
	for _, statement := range body.List {
		if statement.Pos() >= pos {
			return false
		}
		gate, ok := statement.(*ast.IfStmt)
		if !ok || !(readsEnvironment(gate.Init, os) || readsEnvironment(gate.Cond, os)) || len(gate.Body.List) == 0 {
			continue
		}
		switch last := gate.Body.List[len(gate.Body.List)-1].(type) {
		case *ast.ReturnStmt:
			return true
		case *ast.ExprStmt:
			if call, ok := last.X.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(selector.Sel.Name, "Skip") {
					return true
				}
			}
		}
	}
	return false
}

func isBubble(node ast.Node, synctest string) bool {
	call, ok := node.(*ast.CallExpr)
	return ok && synctest != "" && (isCall(call, synctest, "Test") || isCall(call, synctest, "Run"))
}

func readsEnvironment(cond ast.Node, os string) bool {
	if os == "" || cond == nil {
		return false
	}
	found := false
	ast.Inspect(cond, func(node ast.Node) bool {
		if call, ok := node.(ast.Expr); ok && (isCall(call, os, "Getenv") || isCall(call, os, "LookupEnv")) {
			found = true
		}
		return !found
	})
	return found
}

func isCall(expr ast.Expr, pkg, name string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || pkg == "" {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == pkg
}
