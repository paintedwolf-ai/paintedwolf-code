package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestWireAPIExportedFieldsHaveJSONTag prevents implicit Go field names on the wire.
// Embedded, anonymous, and private fields are outside this check.
func TestWireAPIExportedFieldsHaveJSONTag(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	apiDir := filepath.Join(root, "lycaon", "pkg", "api")

	fset := token.NewFileSet()
	entries, err := os.ReadDir(apiDir)
	contractcheck.FailErr(t, "read directory entries", err)

	type violation struct {
		file, structName, field string
	}
	var violations []violation

	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(apiDir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			if st.Fields == nil {
				return true
			}
			for _, f := range st.Fields.List {
				// Embedded (anonymous) field — json handles via the embedded type.
				if len(f.Names) == 0 {
					continue
				}
				// Tag check applies to the whole field declaration; a single tag
				// covers all names on the line.
				tag := ""
				if f.Tag != nil {
					tag = f.Tag.Value
				}
				for _, fname := range f.Names {
					if !fname.IsExported() {
						continue
					}
					if jsonTagValue(tag) == "" {
						violations = append(violations, violation{
							file:       name,
							structName: ts.Name.Name,
							field:      fname.Name,
						})
					}
				}
			}
			return true
		})
	}
	if len(violations) > 0 {
		sort.Slice(violations, func(i, j int) bool {
			if violations[i].file != violations[j].file {
				return violations[i].file < violations[j].file
			}
			if violations[i].structName != violations[j].structName {
				return violations[i].structName < violations[j].structName
			}
			return violations[i].field < violations[j].field
		})
		var b strings.Builder
		b.WriteString("exported wire fields missing json tag (would serialize as Go name on wire):\n")
		for _, v := range violations {
			b.WriteString("  ")
			b.WriteString(v.file)
			b.WriteString(": ")
			b.WriteString(v.structName)
			b.WriteString(".")
			b.WriteString(v.field)
			b.WriteString("\n")
		}
		t.Fatal(b.String())
	}
}

// jsonTagValue extracts the value of the json:"..." tag from a raw struct
// tag literal (including surrounding backticks). Returns "" if absent.
func jsonTagValue(rawTag string) string {
	if rawTag == "" {
		return ""
	}
	// Strip surrounding backticks.
	if len(rawTag) >= 2 && rawTag[0] == '`' && rawTag[len(rawTag)-1] == '`' {
		rawTag = rawTag[1 : len(rawTag)-1]
	}
	// Find json:"...".
	const key = `json:"`
	i := strings.Index(rawTag, key)
	if i < 0 {
		return ""
	}
	rest := rawTag[i+len(key):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// TestWireAPIJSONTagsAreSnakeCase verifies wire struct JSON tags adhere to snake_case.
func TestWireAPIJSONTagsAreSnakeCase(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	apiDir := filepath.Join(root, "lycaon", "pkg", "api")

	fset := token.NewFileSet()
	entries, err := os.ReadDir(apiDir)
	contractcheck.FailErr(t, "read directory entries", err)

	snakePattern := strings.TrimSpace("^[a-z][a-z0-9_]*$")
	var violations []string

	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(apiDir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					continue
				}
				tag := ""
				if f.Tag != nil {
					tag = f.Tag.Value
				}
				val := jsonTagValue(tag)
				key := strings.Split(val, ",")[0]
				if key == "" || key == "-" {
					continue
				}
				matched, _ := filepath.Match(snakePattern, key)
				_ = matched
				for i, r := range key {
					if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9' && i > 0) || (r == '_' && i > 0) {
						continue
					}
					violations = append(violations, name+": "+ts.Name.Name+"."+f.Names[0].Name+` json:"`+key+`"`)
					break
				}
			}
			return true
		})
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("wire struct fields with non-snake_case JSON tags:\n  %s", strings.Join(violations, "\n  "))
	}
}
