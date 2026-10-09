package contract

import (
	"go/ast"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

const peoplePkg = "github.com/lycaon/lycaon/internal/people"

// Only admitted authorship may fall back to the host owner.
var actingFallbacks = map[string]string{
	"internal/session/store/sql_sessions.go:CreateWithStatus": "a root chat the host opens belongs to the host owner",
	"internal/session/store/memory.go:CreateWithStatus":       "memory twin of the SQL session store",
	"internal/session/kick_nudge.go:promptAuthor":             "a prompt without an admission receipt is authored by its request's caller",
	"internal/session/prompt_submission.go:admitPrompt":       "a user prompt admission names its request's caller",
	"internal/editordoc/authorship.go:personActor":            "editor transitions are authored by their client's person",
	"internal/sourceledger/recording.go:operationPerson":      "user-origin source operations without an explicit person",
	"internal/projectsource/source_mutation_store.go:insert":  "user file mutations journal their person",
}

func TestOwnerFallbackIsLimitedToAdmittedAuthorship(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unused := map[string]bool{}
	for key := range actingFallbacks {
		unused[key] = true
	}
	var offences []string
	for _, gf := range corp.Files() {
		rel := filepath.ToSlash(gf.Rel)
		if gf.IsTest || strings.HasPrefix(rel, "internal/people/") {
			continue
		}
		alias := contractcheck.ImportAliasFor(gf.AST, peoplePkg)
		if alias == "" {
			continue
		}
		for _, fn := range funcDecls(gf.AST) {
			if !callsSelector(fn.Body, alias, "Acting") {
				continue
			}
			key := rel + ":" + fn.Name.Name
			if _, ok := actingFallbacks[key]; ok {
				delete(unused, key)
				continue
			}
			offences = append(offences, key)
		}
	}
	sort.Strings(offences)
	contractcheck.FailViolations(t, "people.Acting credits the host owner outside admitted authorship; "+
		"a decision uses people.Deciding and refuses without a caller", offences)
	contractcheck.FailViolations(t, "stale actingFallbacks entries", sortedKeys(unused))
}

func TestAgentToolCallsCarryNoRequestCaller(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "toolexecution"))
	contractcheck.FailErr(t, "load AST corpus", err)
	found := false
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		alias := contractcheck.ImportAliasFor(gf.AST, peoplePkg)
		for _, fn := range funcDecls(gf.AST) {
			if fn.Name.Name != "Invoke" || receiverName(fn) != "Executor" {
				continue
			}
			found = true
			if alias == "" || !callsSelector(fn.Body, alias, "WithoutCaller") {
				t.Errorf("Executor.Invoke must strip the request caller with people.WithoutCaller " +
					"so agent effects are never recorded as a person's")
			}
		}
	}
	if !found {
		t.Fatal("Executor.Invoke not found")
	}
}

// directRouteExemptions register state-changing routes outside the operation
// catalog, so no person action is recorded for them.
var directRouteExemptions = map[string]string{
	"internal/api/harness_control.go":     "harness control plane for driven test stacks",
	"internal/api/harness_preparation.go": "harness control plane for driven test stacks",
}

func TestStateChangingRoutesPassThePersonActionSeam(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "api"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unused := map[string]bool{}
	for rel := range directRouteExemptions {
		unused[rel] = true
	}
	var offences []string
	registrarWraps := false
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		rel := "internal/api/" + filepath.ToSlash(gf.Rel)
		for _, fn := range funcDecls(gf.AST) {
			switch fn.Name.Name {
			case "registerV1Operation":
				registrarWraps = callsMethod(fn.Body, "recordPersonAction")
				continue
			case "registerRootOperation":
				continue
			}
			if !registersStateChangingRoute(fn.Body) {
				continue
			}
			if _, ok := directRouteExemptions[rel]; ok {
				delete(unused, rel)
				continue
			}
			offences = append(offences, rel+":"+fn.Name.Name)
		}
	}
	if !registrarWraps {
		t.Error("registerV1Operation must wrap every handler with recordPersonAction")
	}
	sort.Strings(offences)
	contractcheck.FailViolations(t, "state-changing routes registered outside registerV1Operation", offences)
	contractcheck.FailViolations(t, "stale directRouteExemptions entries", sortedKeys(unused))
}

func TestRootOperationsAreReads(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	methods := catalogMethods(t, root)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "api"))
	contractcheck.FailErr(t, "load AST corpus", err)
	var offences []string
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || contractcheck.CallName(call) != "registerRootOperation" || len(call.Args) < 2 {
				return true
			}
			op, ok := call.Args[1].(*ast.Ident)
			if !ok {
				offences = append(offences, gf.Rel+": registerRootOperation takes a generated operation variable")
				return true
			}
			if method := methods[op.Name]; method != "GET" {
				offences = append(offences, op.Name+" is "+method+"; unauthenticated root operations must be reads")
			}
			return true
		})
	}
	contractcheck.FailViolations(t, "root operations that change state bypass caller binding and person actions", offences)
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

// callsMethod reports a call to any receiver's method named name inside body.
func callsMethod(body ast.Node, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// registersStateChangingRoute finds chi registrations of a literal route with
// a state-changing method, or any Method registration.
func registersStateChangingRoute(body ast.Node) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || found {
			return !found
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		switch sel.Sel.Name {
		case "Post", "Put", "Patch", "Delete":
			found = strings.HasPrefix(contractcheck.AstStringLit(call.Args[0]), "/")
		case "Method", "MethodFunc":
			found = !strings.EqualFold(contractcheck.AstStringLit(call.Args[0]), "GET")
		}
		return !found
	})
	return found
}

// catalogMethods maps each generated operation variable to its HTTP method.
func catalogMethods(t *testing.T, root string) map[string]string {
	t.Helper()
	_, files := contractcheck.ParseNonTestGoTree(t, filepath.Join(root, "lycaon", "internal", "api"))
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				lit, ok := value.Values[0].(*ast.CompositeLit)
				if !ok || contractcheck.CompositeLitTypeName(lit.Type) != "generatedOperation" {
					continue
				}
				for _, element := range lit.Elts {
					kv, ok := element.(*ast.KeyValueExpr)
					if key, isIdent := kv.Key.(*ast.Ident); ok && isIdent && key.Name == "Method" {
						out[value.Names[0].Name] = contractcheck.AstStringLit(kv.Value)
					}
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no generated operations found")
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
