package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestVisualDurableNoHeuristicsContract locks promotion / dedup /
// section-inclusion on machine state only (evidence_handle, id, data presence).
// Forbids prose matchers and workflow-id switches in the visual+report path.
func TestVisualDurableNoHeuristicsContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dirs := []string{
		filepath.Join(root, "lycaon", "internal", "visual"),
		filepath.Join(root, "lycaon", "internal", "report"),
		filepath.Join(root, "lycaon", "internal", "guidance"),
		filepath.Join(root, "lycaon", "internal", "api"),
	}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			base := filepath.Base(path)
			if strings.HasSuffix(path, "_test.go") || strings.Contains(base, "fixture") {
				return nil
			}
			// Only the visual/report assembly surfaces are gated.
			switch {
			case strings.Contains(path, string(filepath.Separator)+"visual"+string(filepath.Separator)):
				// production visual store / cover / ledger
			case base == "sections.go" || base == "report.go" || base == "input.go":
				// report section inclusion / partition
			case base == "closeout_present.go":
				// promotion from tool-result Visual ids
			case base == "workflow_report_handler.go":
				// after-the-fact artifact assembly
			default:
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "strings" {
					return true
				}
				switch sel.Sel.Name {
				case "Contains", "ContainsAny", "ContainsRune", "HasPrefix", "HasSuffix", "Index", "IndexAny":
					for _, arg := range call.Args {
						lit, ok := arg.(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						needle := strings.Trim(lit.Value, `"`)
						if looksLikeProseNeedle(needle) {
							t.Errorf("%s: forbidden prose matcher strings.%s(%q) — branch on machine state only",
								path, sel.Sel.Name, needle)
						}
					}
				}
				return true
			})
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(src)
			if base == "sections.go" || base == "report.go" || base == "closeout_present.go" {
				for _, banned := range []string{
					"WorkflowID ==", "workflowID ==", `WorkflowID == "`, "workflow_id ==",
				} {
					if strings.Contains(text, banned) {
						t.Errorf("%s: forbidden workflow-id section branch (%q)", path, banned)
					}
				}
			}
			return nil
		})
		testutil.FailErr(t, "walk "+dir, err)
	}
}

// looksLikeProseNeedle flags English-ish needles used to infer intent from
// closeout/synthesis text. Technical tokens (extensions, mime, path fragments,
// HTML sniff) are machine-state checks and allowed.
func looksLikeProseNeedle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	for _, ok := range []string{
		"image/", "application/", "text/", "http", "refs.json", "page#",
		"/.paintedwolf", "<!", "</", "bytes",
	} {
		if strings.Contains(lower, ok) {
			return false
		}
	}
	// File extensions / dotted tokens without spaces.
	if strings.HasPrefix(s, ".") && !strings.Contains(s, " ") {
		return false
	}
	// Structural markers carry no words: "## ", "- [ ] ", "```". Trimming a
	// markdown heading is reading document structure, not inferring intent, and
	// the space inside the marker must not make it read as English.
	if isPunctuationMarker(s) {
		return false
	}
	// Prose: whitespace (multi-word English) or sentence punctuation.
	if strings.Contains(s, " ") {
		return true
	}
	if strings.ContainsAny(s, ";!") && len(s) > 6 {
		return true
	}
	return false
}

// isPunctuationMarker reports whether s carries no letters or digits once
// whitespace is removed — a delimiter or markup token rather than words.
func isPunctuationMarker(s string) bool {
	seen := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		seen = true
	}
	return seen
}
