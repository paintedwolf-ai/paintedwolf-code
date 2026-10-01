package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The prompt renderer passes every template the variables its caller assembled,
// selecting neither by template ref nor by map keys; a dropped variable renders
// as a silent empty branch. Flags any ref read or per-key decision in
// executeTemplate and its helper.
func TestPromptRendererPassesVarsUnfiltered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "prompts", "pongo_render.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse pongo_render.go", err)

	for _, finding := range scanRenderContextFiltering(fset, file) {
		t.Error(finding)
	}
}

// refFilteringCalls decide something from a template ref string.
var refFilteringCalls = map[string]bool{
	"HasPrefix": true,
	"HasSuffix": true,
	"Contains":  true,
	"EqualFold": true,
}

func scanRenderContextFiltering(fset *token.FileSet, file *ast.File) []string {
	var findings []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Name.Name != "executeTemplate" && fn.Name.Name != "toPongoContext" {
			continue
		}
		at := func(pos token.Pos) string {
			return "internal/prompts/pongo_render.go:" + strconv.Itoa(fset.Position(pos).Line)
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok || !refFilteringCalls[sel.Sel.Name] {
					return true
				}
				findings = append(findings, at(v.Pos())+": "+fn.Name.Name+
					" branches on the template ref ("+sel.Sel.Name+
					") — every ref gets the caller's variables")
			case *ast.IndexExpr:
				// A per-key lookup while building the context is a filter,
				// wherever the key list itself lives.
				if id, ok := v.X.(*ast.Ident); ok && strings.HasSuffix(id.Name, "Keys") {
					findings = append(findings, at(v.Pos())+": "+fn.Name.Name+
						" selects context keys from "+id.Name+
						" — pass the caller's map whole")
				}
			}
			return true
		})
	}
	return findings
}

// A rule that has never matched is indistinguishable from a clean tree.
func TestPromptVarsRuleFiresOnSyntheticViolations(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"ref prefix branch": {
			src: "package prompts\n" +
				"func (e *FileTemplateEngine) executeTemplate(ref string, data map[string]any) (string, error) {\n" +
				"\tif strings.HasPrefix(ref, \"inject/\") { return \"\", nil }\n\treturn \"\", nil\n}\n",
			want: "branches on the template ref",
		},
		"key set lookup": {
			src: "package prompts\n" +
				"func toPongoContext(data map[string]any) pongo2.Context {\n" +
				"\tfor k := range data { _ = renderKeys[k] }\n\treturn nil\n}\n",
			want: "selects context keys",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "synthetic.go", tc.src, 0)
			testutil.FailErr(t, "parse synthetic source", err)
			findings := scanRenderContextFiltering(fset, file)
			if len(findings) != 1 || !strings.Contains(findings[0], tc.want) {
				t.Fatalf("want one finding containing %q, got %v", tc.want, findings)
			}
		})
	}

	t.Run("passing the map whole is clean", func(t *testing.T) {
		t.Parallel()
		src := "package prompts\n" +
			"func toPongoContext(data map[string]any) pongo2.Context {\n" +
			"\tctx := make(pongo2.Context, len(data))\n" +
			"\tfor k, v := range data { ctx[k] = v }\n\treturn ctx\n}\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
		testutil.FailErr(t, "parse synthetic source", err)
		if findings := scanRenderContextFiltering(fset, file); len(findings) != 0 {
			t.Fatalf("flagged the unfiltered form: %v", findings)
		}
	})
}
