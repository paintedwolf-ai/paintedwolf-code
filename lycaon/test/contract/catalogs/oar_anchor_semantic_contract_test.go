package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSharedObservationFactsRegistered(t *testing.T) {
	t.Parallel()
	declared := map[string]bool{}
	for _, name := range scanOARDeclaredFacts(t) {
		declared[name] = true
	}
	for _, tc := range []struct {
		name, condition string
		observe         func(*oar.GuardContext)
	}{
		{"is_directory", "is_directory", func(gc *oar.GuardContext) { gc.Rejection.IsDirectory = true }},
		{"not_found", "not_found", func(gc *oar.GuardContext) { gc.Rejection.NotFound = true }},
		{"path_denied", "paintedwolf.path_denied", func(gc *oar.GuardContext) { gc.Rejection.PathDenied = true }},
		{"reject_observation", "paintedwolf.reject_observation == 'path_escape'", func(gc *oar.GuardContext) { gc.Rejection.RejectObservation = "path_escape" }},
		{"policy_denied", "paintedwolf.policy_denied", func(gc *oar.GuardContext) { gc.Rejection.PolicyDenied = true }},
		{"command_not_argv", "paintedwolf.command_not_argv", func(gc *oar.GuardContext) { gc.Invocation.CommandNotArgv = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !declared[tc.name] {
				t.Fatalf("catalogue missing %q", tc.name)
			}
			gc := oar.NewGuardContext()
			matched, err := oar.EvaluateCondition(tc.condition, gc)
			contractcheck.FailErr(t, "evaluate absent observation", err)
			if matched {
				t.Fatal("unobserved rejection fact matched")
			}
			tc.observe(gc)
			matched, err = oar.EvaluateCondition(tc.condition, gc)
			contractcheck.FailErr(t, "evaluate structured observation", err)
			if !matched {
				t.Fatal("observed rejection fact is absent from the condition environment")
			}
		})
	}
}

// scanOARDeclaredFacts reads the fact names declared in oar/catalogue.go.
func scanOARDeclaredFacts(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "oar", "catalogue.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse oar/catalogue.go", err)
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "factDecls" || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range lit.Elts {
				row, ok := elt.(*ast.CompositeLit)
				if !ok || len(row.Elts) == 0 {
					continue
				}
				if name := contractcheck.AstStringLit(row.Elts[0]); name != "" {
					names = append(names, name)
				}
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("no factDecls parsed from oar/catalogue.go")
	}
	return names
}
