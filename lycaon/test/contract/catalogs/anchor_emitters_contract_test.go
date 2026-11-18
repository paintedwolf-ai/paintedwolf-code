package contract

// Static reference closure complements runtime advisory-delivery tests.
// A reference alone does not prove that an occurrence fires or reaches a model.

import (
	"go/ast"
	"go/token"
	"path"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// anchorDeclFiles are the Go files that declare named anchor ids, keyed by
// corpus path under lycaon/internal, with their package import path.
var anchorDeclFiles = map[string]string{
	"coordinator/anchor/id.go": "github.com/lycaon/lycaon/internal/coordinator/anchor",
	"oar/block.go":             "github.com/lycaon/lycaon/internal/oar",
}

// dottedAnchorConsts maps each top-level constant whose value is a dotted
// string literal, directly or through a one-argument conversion, to that id.
func dottedAnchorConsts(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				value := vs.Values[i]
				if call, ok := value.(*ast.CallExpr); ok && len(call.Args) == 1 {
					value = call.Args[0]
				}
				if id, ok := stringLiteral(value); ok && path.Ext(id) != "" {
					out[name.Name] = id
				}
			}
		}
	}
	return out
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

// declaringImports maps the local name of each imported declaring package to its path.
func declaringImports(file *ast.File) map[string]string {
	declaring := map[string]bool{}
	for _, importPath := range anchorDeclFiles {
		declaring[importPath] = true
	}
	out := map[string]string{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !declaring[importPath] {
			continue
		}
		name := path.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		out[name] = importPath
	}
	return out
}

// TestEveryCatalogAnchorHasProductionReference requires every catalog anchor to
// be named by production Go (a qualified reference to its declared constant, a
// bare reference inside the declaring package, or its id as a string literal)
// or bound by a stock workflow manifest.
func TestEveryCatalogAnchorHasProductionReference(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	catalogIDs, _ := loadCatalogAnchors(t, root)
	if len(catalogIDs) == 0 {
		t.Fatal("anchor catalog declares no anchors")
	}
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal Go corpus", err)

	consts := map[string]map[string]string{}
	packageDirs := map[string]string{}
	for rel, importPath := range anchorDeclFiles {
		packageDirs[path.Dir(rel)] = importPath
		for _, f := range corpus.Production() {
			if f.Rel == rel {
				consts[importPath] = dottedAnchorConsts(f.AST)
			}
		}
		if len(consts[importPath]) == 0 {
			t.Fatalf("%s declares no anchor constants", rel)
		}
	}

	referenced := map[string]bool{}
	for _, f := range corpus.Production() {
		if _, decl := anchorDeclFiles[f.Rel]; decl {
			continue
		}
		imports := declaringImports(f.AST)
		local := consts[packageDirs[path.Dir(f.Rel)]]
		selectorNames := map[*ast.Ident]bool{}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				selectorNames[node.Sel] = true
				if x, ok := node.X.(*ast.Ident); ok {
					if id, ok := consts[imports[x.Name]][node.Sel.Name]; ok {
						referenced[id] = true
					}
				}
			case *ast.Ident:
				if id, ok := local[node.Name]; ok && !selectorNames[node] {
					referenced[id] = true
				}
			case *ast.BasicLit:
				if id, ok := stringLiteral(node); ok && catalogIDs[id] {
					referenced[id] = true
				}
			}
			return true
		})
	}

	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "catalog for consumers", err)
	manifests, _, err := workflowdef.LoadManifestsFromCatalog(catalog)
	contractcheck.FailErr(t, "load stock workflow manifests", err)
	if len(manifests) == 0 {
		t.Fatal("no stock workflow manifests loaded")
	}
	for _, m := range manifests {
		for _, inject := range m.Injects {
			referenced[inject.On] = true
		}
		for _, phase := range m.PhaseDefs {
			referenced[phase.OnReenter.InjectKick] = true
		}
	}

	var missing []string
	for id := range catalogIDs {
		if !referenced[id] {
			missing = append(missing, id)
		}
	}
	contractcheck.FailViolations(t, "catalog anchors with no production or manifest reference", missing)
}
