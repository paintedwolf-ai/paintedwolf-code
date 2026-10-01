package wirespec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func DiscoverAPIStringEnums(repoRoot string) (map[string][]string, error) {
	apiDir := filepath.Join(repoRoot, "lycaon", "pkg", "api")
	entries, err := os.ReadDir(apiDir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	var files []*ast.File
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(apiDir, name)
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	stringTypes := make(map[string]bool)
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				ident, ok := ts.Type.(*ast.Ident)
				if ok && ident.Name == "string" {
					stringTypes[ts.Name.Name] = true
				}
			}
		}
	}

	values := make(map[string][]string)
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				enumType := constEnumType(vs)
				if enumType == "" || !stringTypes[enumType] {
					continue
				}
				for i, name := range vs.Names {
					if name.Name == "_" {
						continue
					}
					if len(vs.Values) <= i {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					val := strings.Trim(lit.Value, `"`)
					// A zero-value constant names an omitted field, not a wire value.
					if val == "" {
						continue
					}
					values[enumType] = appendUnique(values[enumType], val)
				}
			}
		}
	}

	for k := range values {
		sort.Strings(values[k])
	}
	return values, nil
}

func constEnumType(vs *ast.ValueSpec) string {
	if vs.Type != nil {
		if ident, ok := vs.Type.(*ast.Ident); ok {
			return ident.Name
		}
	}
	return ""
}

func appendUnique(ss []string, v string) []string {
	for _, existing := range ss {
		if existing == v {
			return ss
		}
	}
	return append(ss, v)
}

func GoEnumValues[T ~string](values ...T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
