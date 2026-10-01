package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var inlineDDLForbidden = []string{
	"ALTER TABLE",
	"CREATE TABLE",
	"CREATE INDEX",
	"DROP COLUMN",
}

// scanInternalDBInlineDDL finds schema DDL in production Go files.
func scanInternalDBInlineDDL(dbDir string) ([]string, error) {
	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(dbDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if strings.HasSuffix(name, ".sql.go") || name == "models.go" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			unquoted := strings.Trim(lit.Value, "`\"")
			for _, pat := range inlineDDLForbidden {
				if strings.Contains(unquoted, pat) {
					pos := fset.Position(lit.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d: contains %q", pos.Filename, pos.Line, pat))
					break
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}
