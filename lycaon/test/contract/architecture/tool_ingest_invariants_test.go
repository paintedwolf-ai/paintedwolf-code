package contract

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oarcopy"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// walkGoFiles visits every non-test .go file under root.
func walkGoFiles(t *testing.T, root string, fn func(path string, fset *token.FileSet, file *ast.File)) {
	t.Helper()
	corpus, err := contractcheck.LoadGoASTCorpus(root)
	contractcheck.FailErr(t, "load Go corpus", err)
	for _, file := range corpus.Files() {
		if !file.IsTest {
			fn(file.Path, corpus.Fset, file.AST)
		}
	}
}

// TestToolFileIngestIsSizeBounded requires size checks before whole-file reads.
func TestToolFileIngestIsSizeBounded(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "tools")
	var violations []string

	walkGoFiles(t, root, func(path string, fset *token.FileSet, file *ast.File) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var slurps []token.Pos
			guarded := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch name := contractcheck.CallName(call); {
				case name == "os.ReadFile":
					slurps = append(slurps, call.Lparen)
				case strings.HasSuffix(name, ".Size"), name == "readContentCapped",
					strings.HasSuffix(name, "SpillBytes"):
					// Either path proves the read is bounded.
					guarded = true
				}
				return true
			})
			if guarded {
				continue
			}
			for _, pos := range slurps {
				violations = append(violations,
					fset.Position(pos).String()+" in "+fn.Name.Name)
			}
		}
	})

	if len(violations) > 0 {
		t.Fatalf("unbounded file ingest — stat the file first or call readContentCapped:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

var templateVarRe = regexp.MustCompile(`{{\s*(\w+(?:\.\w+)*)\s*}}`)

// rejectionCopyFacts combines handler observations with invocation metadata.
func rejectionCopyFacts(keys map[string]bool) map[string]any {
	data := make(map[string]any, len(keys))
	for key := range keys {
		data[key] = "observed"
	}
	reject := tools.CompleteFailureMetadata(&tools.ToolReject{Data: data}, "invoked_tool", "native")
	return oarcopy.FactsFromData(reject.Data)
}

// rejectEmission records one statically resolved ToolReject.
type rejectEmission struct {
	pos  string
	keys map[string]bool
}

// collectRejectEmissions records keys from inline ToolReject data maps.
func collectRejectEmissions(t *testing.T, root string) map[string][]rejectEmission {
	t.Helper()
	out := map[string][]rejectEmission{}
	walkGoFiles(t, root, func(path string, fset *token.FileSet, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isToolRejectType(lit.Type) {
				return true
			}
			var code string
			var data ast.Expr
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Code":
					if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						code, _ = strconv.Unquote(bl.Value)
					}
				case "Data":
					data = kv.Value
				}
			}
			if code == "" {
				return true
			}
			keys := map[string]bool{}
			if dm, ok := data.(*ast.CompositeLit); ok {
				for _, elt := range dm.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if bl, ok := kv.Key.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						k, _ := strconv.Unquote(bl.Value)
						keys[k] = true
					}
				}
			} else if data != nil {
				return true // Variable-backed data cannot be resolved statically.
			}
			out[code] = append(out[code], rejectEmission{
				pos: fset.Position(lit.Pos()).String(), keys: keys,
			})
			return true
		})
	})
	return out
}

// isToolRejectType limits the scan to rendered tool rejections.
func isToolRejectType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "ToolReject"
	case *ast.Ident:
		return t.Name == "ToolReject"
	}
	return false
}

// TestRejectTemplateVarsAreSupplied matches template variables to emitted data.
func TestRejectTemplateVarsAreSupplied(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	units, err := filepath.Glob(filepath.Join(root, "lycaon", "config", "packs", "*", "*", "policy", "*.yaml"))
	if err != nil {
		t.Fatalf("glob policy units: %v", err)
	}
	if len(units) == 0 {
		t.Fatal("no policy units found; invariant would pass vacuously")
	}
	emissions := collectRejectEmissions(t, filepath.Join(root, "lycaon", "internal"))

	var problems []string
	checked := 0
	for _, unit := range units {
		raw, rerr := os.ReadFile(unit)
		if rerr != nil {
			t.Fatalf("read %s: %v", unit, rerr)
		}
		code := strings.TrimSuffix(filepath.Base(unit), ".yaml")
		// Control-flow units may bind or render variables conditionally.
		if strings.Contains(string(raw), "{%") {
			continue
		}
		// Scenario values contain no template placeholders.
		required := map[string]bool{}
		for _, m := range templateVarRe.FindAllStringSubmatch(string(raw), -1) {
			required[m[1]] = true
		}
		if len(required) == 0 {
			continue
		}
		sites := emissions[code]
		if len(sites) == 0 {
			continue // Non-literal emission paths are outside this scan.
		}
		checked++
		for _, site := range sites {
			facts := rejectionCopyFacts(site.keys)
			for v := range required {
				if _, supplied := facts[v]; supplied {
					continue
				}
				problems = append(problems,
					code+": template var {{ "+v+" }} not supplied at "+site.pos)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no policy unit was actually checked; invariant would pass vacuously")
	}
	if len(problems) > 0 {
		t.Fatalf("reject templates interpolate variables production never supplies (these render empty):\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// normalizedBody creates an identifier-neutral function fingerprint.
func normalizedBody(fn *ast.FuncDecl) (string, int) {
	var b strings.Builder
	stmts := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		if _, ok := n.(ast.Stmt); ok {
			stmts++
		}
		switch node := n.(type) {
		case *ast.Ident:
			b.WriteString("@")
		case *ast.BasicLit:
			b.WriteString(node.Value)
		default:
			fmt.Fprintf(&b, "%T", n)
		}
		return true
	})
	return b.String(), stmts
}

// TestNoDuplicatedPrimitiveLogic detects copied non-trivial functions.
func TestNoDuplicatedPrimitiveLogic(t *testing.T) {
	// Small shapes collide too often to identify copied logic.
	const minStmts = 10
	// Tool packages form one comparison domain.
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "tools")
	type site struct{ pkg, name, pos string }
	bodies := map[string][]site{}

	walkGoFiles(t, root, func(path string, fset *token.FileSet, file *ast.File) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key, stmts := normalizedBody(fn)
			if stmts < minStmts || countLiteralDigits(key) < 2 {
				continue
			}
			bodies[key] = append(bodies[key], site{
				pkg: file.Name.Name, name: fn.Name.Name, pos: fset.Position(fn.Pos()).String(),
			})
		}
	})

	var dupes []string
	for _, sites := range bodies {
		pkgs := map[string]bool{}
		for _, s := range sites {
			pkgs[s.pkg] = true
		}
		if len(pkgs) < 2 {
			continue
		}
		var where []string
		for _, s := range sites {
			where = append(where, s.pkg+"."+s.name+" ("+s.pos+")")
		}
		sort.Strings(where)
		dupes = append(dupes, strings.Join(where, " == "))
	}
	if len(dupes) > 0 {
		sort.Strings(dupes)
		t.Fatalf("identical logic duplicated across packages — extract one definition so copies cannot drift:\n  %s",
			strings.Join(dupes, "\n  "))
	}
}

// countLiteralDigits measures whether a fingerprint has distinctive constants.
func countLiteralDigits(key string) int {
	n := 0
	for _, r := range key {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n
}
