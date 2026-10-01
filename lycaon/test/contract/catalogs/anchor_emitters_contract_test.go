package contract

// Static reference closure complements runtime advisory-delivery tests.
// A reference alone does not prove that an occurrence fires or reaches a model.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// anchorConstDecls maps const name → dotted id from a Go const file.
func anchorConstDecls(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse "+path, err)
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if i >= len(spec.Values) {
				continue
			}
			var lit *ast.BasicLit
			switch v := spec.Values[i].(type) {
			case *ast.BasicLit:
				lit = v
			case *ast.CallExpr: // ID("...") style
				if len(v.Args) == 1 {
					if bl, ok := v.Args[0].(*ast.BasicLit); ok {
						lit = bl
					}
				}
			}
			if lit == nil || lit.Kind != token.STRING {
				continue
			}
			id := strings.Trim(lit.Value, `"`)
			if strings.Contains(id, ".") {
				out[name.Name] = id
			}
		}
		return true
	})
	return out
}

func TestEveryCatalogAnchorHasProductionReference(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	catalogIDs, _ := loadCatalogAnchors(t, root)

	declFiles := map[string]string{
		filepath.Join(lycaonRoot, "internal", "coordinator", "anchor", "id.go"): "anchor",
		filepath.Join(lycaonRoot, "internal", "oar", "block.go"):                "oar",
	}
	// dotted id → selector spellings that count as a reference.
	refSpellings := map[string][]string{}
	for path, pkg := range declFiles {
		for constName, id := range anchorConstDecls(t, path) {
			refSpellings[id] = append(refSpellings[id], pkg+"."+constName)
		}
	}

	// Anchors referenced only through manifest/binding YAML resolve via
	// ParseID on the dotted literal, so the literal itself also counts.
	for id := range catalogIDs {
		refSpellings[id] = append(refSpellings[id], `"`+id+`"`)
	}

	referenced := map[string]bool{}
	skipDecl := map[string]bool{}
	for path := range declFiles {
		skipDecl[path] = true
	}
	err := filepath.Walk(filepath.Join(lycaonRoot, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if skipDecl[path] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		body := string(raw)
		for id, spellings := range refSpellings {
			if referenced[id] {
				continue
			}
			for _, s := range spellings {
				if strings.Contains(body, s) {
					referenced[id] = true
					break
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal", err)

	// Workflow manifests may bind an anchor by dotted id (on: / inject_kick).
	err = filepath.Walk(filepath.Join(lycaonRoot, "config", "packs"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "workflow.yaml") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		body := string(raw)
		for id := range catalogIDs {
			if !referenced[id] && strings.Contains(body, id) {
				referenced[id] = true
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk workflow manifests", err)

	var missing []string
	for id := range catalogIDs {
		if !referenced[id] {
			missing = append(missing, id+": no production or manifest reference")
		}
	}
	contractcheck.FailViolations(t, "catalog anchors with no production reference", missing)
}
