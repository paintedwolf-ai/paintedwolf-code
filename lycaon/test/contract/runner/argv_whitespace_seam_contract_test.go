package contract

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/configlayout"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// argvWhitespaceSeam defines the package's whitespace rules.
const argvWhitespaceSeam = "internal/argv/whitespace.go"

// argvPackagePrefix scopes this contract to the tokenizer.
const argvPackagePrefix = "internal/argv/"

// The parser and renderer share one whitespace vocabulary.
func TestArgvHasOneWhitespaceVocabulary(t *testing.T) {
	t.Parallel()
	corp, err := contractcheck.LoadGoASTCorpus(configlayout.FindModuleRoot())
	contractcheck.FailErr(t, "load Go AST corpus", err)

	var violations []string
	examined := 0

	for _, f := range corp.Files() {
		if !strings.HasPrefix(f.Rel, argvPackagePrefix) || f.IsTest || f.Rel == argvWhitespaceSeam {
			continue
		}
		examined++
		ast.Inspect(f.AST, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				// These helpers would redefine the shared whitespace rule.
				switch calleeName(v.Fun) {
				case "TrimSpace", "TrimLeft", "TrimRight", "IsSpace", "Fields":
					violations = append(violations, argvSeamViolation(corp.Fset, v.Pos(), f.Rel,
						"calls "+calleeName(v.Fun)+" — ask trimCommandLine, trimmedEdge, or argSeparator"))
				}
			case *ast.BasicLit:
				if v.Kind == token.CHAR && isWhitespaceRuneLiteral(v.Value) {
					violations = append(violations, argvSeamViolation(corp.Fset, v.Pos(), f.Rel,
						"names the whitespace literal "+v.Value+" — ask argSeparator or trimmedEdge"))
				}
			}
			return true
		})
	}

	if examined == 0 {
		t.Fatalf("no non-test files examined outside %s; the scan is not testing anything", argvWhitespaceSeam)
	}
	contractcheck.FailViolations(t, "internal/argv decides whitespace outside its seam", violations)
}

func argvSeamViolation(fset *token.FileSet, pos token.Pos, rel, why string) string {
	return fmt.Sprintf("%s (%s): %s", rel, fset.Position(pos), why)
}

// isWhitespaceRuneLiteral recognizes Go space-rune literals.
func isWhitespaceRuneLiteral(lit string) bool {
	unquoted, err := strconv.Unquote(lit)
	if err != nil {
		return false
	}
	runes := []rune(unquoted)
	return len(runes) == 1 && unicode.IsSpace(runes[0])
}
