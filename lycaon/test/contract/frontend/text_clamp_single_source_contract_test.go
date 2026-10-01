package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Display text is cut only by internal/runeclamp, which cuts on rune boundaries.
// The scan flags a string sliced by index with an elision marker appended, which
// can emit invalid UTF-8.
var clampMarkers = []string{"…", "..."}

// clampScanSkipDirs are trees with no display text to cut.
var clampScanSkipDirs = map[string]bool{
	"runeclamp":    true, // the source
	"testdata":     true,
	"node_modules": true,
}

func TestElisionMarkersComeFromSingleSource(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offences []string

	// Shipped Go trees only: config/ carries vendored scanner fixtures that are not valid Go.
	for _, tree := range []string{"lycaon/internal", "lycaon/cmd", "lycaon/pkg"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(tree)), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if clampScanSkipDirs[entry.Name()] || strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)

			ast.Inspect(file, func(n ast.Node) bool {
				bin, ok := n.(*ast.BinaryExpr)
				if !ok || bin.Op != token.ADD {
					return true
				}
				if !isElisionMarker(bin.Y) && !isElisionMarker(bin.X) {
					return true
				}
				if !slicesAString(bin.X) && !slicesAString(bin.Y) {
					return true
				}
				offences = append(offences, fmt.Sprintf("%s:%d", rel, fset.Position(bin.Pos()).Line))
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "walk "+tree, err)
	}

	if len(offences) > 0 {
		t.Fatalf("text cut by byte offset with an elision marker appended — use internal/runeclamp "+
			"(Clamp/Fit for a rune budget, ClampBytes/ClampBytesTail/ClampBytesMiddle for a byte one):\n  %s",
			strings.Join(offences, "\n  "))
	}
}

func isElisionMarker(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	for _, marker := range clampMarkers {
		if lit.Value == `"`+marker+`"` {
			return true
		}
	}
	return false
}

// slicesAString reports whether expr indexes into something with a high bound.
// Byte offsets and rune offsets both count: the first lands mid-rune, and the
// second reimplements Clamp with its own idea of where the marker goes.
func slicesAString(expr ast.Expr) bool {
	slice, ok := expr.(*ast.SliceExpr)
	return ok && slice.High != nil
}
