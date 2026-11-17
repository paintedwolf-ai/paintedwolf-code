package contract

import (
	"go/ast"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const peoplePkg = "github.com/lycaon/lycaon/internal/people"

// agentInvocationEntries are the executor doors through which a model's call
// reaches an owner; each enters through the shared invocation context.
var agentInvocationEntries = []string{"Invoke", "InvokeNative", "ApplyProgramProgress"}

func TestAgentToolCallsCarryNoRequestCaller(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "toolexecution"))
	contractcheck.FailErr(t, "load AST corpus", err)
	stripped := false
	entries := map[string]bool{}
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		alias := contractcheck.ImportAliasFor(gf.AST, peoplePkg)
		for _, fn := range funcDecls(gf.AST) {
			if fn.Name.Name == "invocationContext" && fn.Recv == nil {
				stripped = alias != "" && callsSelector(fn.Body, alias, "WithoutCaller")
				continue
			}
			if receiverName(fn) == "Executor" && slices.Contains(agentInvocationEntries, fn.Name.Name) {
				entries[fn.Name.Name] = callsFunction(fn.Body, "invocationContext")
			}
		}
	}
	if !stripped {
		t.Error("invocationContext must strip the request caller with people.WithoutCaller " +
			"so agent effects are never recorded as a person's")
	}
	for _, name := range agentInvocationEntries {
		if ok, found := entries[name]; !found {
			t.Errorf("Executor.%s not found", name)
		} else if !ok {
			t.Errorf("Executor.%s must enter through invocationContext, which strips the request caller", name)
		}
	}
}

func callsFunction(body ast.Node, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

func TestEveryOperationDeclaresWhetherItIsAPersonAction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	contractcheck.FailErr(t, "read docs/openapi.yaml", err)
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	contractcheck.FailErr(t, "parse docs/openapi.yaml", yaml.Unmarshal(data, &doc))
	var offences []string
	for path, item := range doc.Paths {
		for method, node := range item {
			var op struct {
				OperationID  string `yaml:"operationId"`
				PersonAction string `yaml:"x-person-action"`
			}
			if node.Decode(&op) != nil || op.OperationID == "" {
				continue
			}
			label := strings.ToUpper(method) + " " + path + " (" + op.OperationID + ")"
			switch strings.ToUpper(method) {
			case "GET", "HEAD", "OPTIONS":
				if op.PersonAction != "" {
					offences = append(offences, label+": reads are never recorded")
				}
			default:
				if op.PersonAction != "record" && op.PersonAction != "none" {
					offences = append(offences, label+": declare x-person-action: record or none")
				}
			}
		}
	}
	sort.Strings(offences)
	contractcheck.FailViolations(t, "operations without a person-action declaration", offences)
}

func funcDecls(f *ast.File) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			out = append(out, fn)
		}
	}
	return out
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// callsSelector reports a call to pkg.name inside body.
func callsSelector(body ast.Node, pkg, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == pkg {
				found = true
			}
		}
		return !found
	})
	return found
}
