package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestNoExportedMutableSliceOrMapGlobals: shipped code under lycaon/internal, pkg, and
// cmd exports no `var X = []T{…}` or `map[K]V{…}`, whose backing data every importer
// could mutate. Composites are exposed through copy- or lookup-returning functions.
func TestNoExportedMutableSliceOrMapGlobals(t *testing.T) {
	t.Parallel()
	codeRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(codeRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata", "test":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(codeRoot, path)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !name.IsExported() {
						continue
					}
					kind := compositeKind(vs.Type)
					if kind == "" && i < len(vs.Values) {
						kind = compositeKind(vs.Values[i])
					}
					if kind != "" {
						violations = append(violations,
							rel+": exported mutable "+kind+" global "+name.Name+" — unexport and expose a copy/lookup accessor")
					}
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk lycaon for exported var declarations", err)
	contractcheck.FailViolations(t, "exported mutable slice/map package globals", violations)
}

// compositeKind reports "slice/array" or "map" for a slice/array/map type or composite
// literal, and "" for anything else (scalars, sentinel errors, regexps, structs).
func compositeKind(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.ArrayType:
		return "slice/array"
	case *ast.MapType:
		return "map"
	case *ast.CompositeLit:
		return compositeKind(t.Type)
	}
	return ""
}
