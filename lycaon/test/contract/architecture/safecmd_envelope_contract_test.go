package contract

import (
	"go/ast"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// envelopeOnlyNativeFiles are the production sources for the six envelope-only
// analysis tools. They must consume safecmd for path resolve / shared caps /
// confine scaffolding — not re-declare those arms locally.
var envelopeOnlyNativeFiles = []string{
	"internal/tools/native/survey/stat.go",
	"internal/tools/native/survey/wc.go",
	"internal/tools/native/survey/list_dir.go",
	"internal/tools/native/survey/list_dir_altitude.go",
	"internal/tools/native/survey/find.go",
	"internal/tools/native/survey/find_walk.go",
	"internal/tools/native/survey/grep.go",
	"internal/tools/native/survey/grep_scope_guard.go",
	"internal/tools/native/jq/jq.go",
	"internal/tools/native/jq/decode.go",
}

// TestEnvelopeOnlyToolsUseSafecmdAST fails when an envelope-only tool file
// re-declares path resolution, confinement, or the shared Caps const values
// outside safecmd.
func TestEnvelopeOnlyToolsUseSafecmdAST(t *testing.T) {
	t.Parallel()
	lycaonRoot := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	corp, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	contractcheck.FailErr(t, "load AST corpus", err)

	want := map[string]struct{}{}
	for _, rel := range envelopeOnlyNativeFiles {
		want[filepath.ToSlash(rel)] = struct{}{}
	}

	var violations []string
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		rel := filepath.ToSlash(gf.Rel)
		if _, ok := want[rel]; !ok {
			continue
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			banned := false
			switch pkg.Name {
			case "projectpaths":
				banned = sel.Sel.Name == "ResolveRead"
			case "confine":
				banned = sel.Sel.Name == "DefaultConfinement"
			case "toolkit":
				banned = sel.Sel.Name == "PathEscapeReject"
			}
			if !banned {
				return true
			}
			pos := corp.Fset.Position(sel.Pos())
			violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+" "+pkg.Name+"."+sel.Sel.Name)
			return true
		})

		// Cap consts live in safecmd; they must not be re-declared as local
		// numeric literals.
		for _, decl := range gf.AST.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if name == nil {
						continue
					}
					n := name.Name
					if strings.HasPrefix(n, "host") && (strings.Contains(n, "Max") || strings.Contains(n, "Timeout") || strings.Contains(n, "Bytes") || strings.Contains(n, "Depth") || strings.Contains(n, "Scan") || strings.Contains(n, "Zoom") || strings.Contains(n, "Entries") || strings.Contains(n, "Paths") || strings.Contains(n, "Files") || strings.Contains(n, "Matches") || strings.Contains(n, "Results") || strings.Contains(n, "Pattern") || strings.Contains(n, "Context") || strings.Contains(n, "Recursive")) {
						// Allow package vars that tests mutate (hostGrepMaxFileBytes).
						if gen.Tok.String() == "var" && n == "hostGrepMaxFileBytes" {
							continue
						}
						pos := corp.Fset.Position(name.Pos())
						violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+" redeclared "+n+" (use safecmd)")
					}
				}
			}
		}
	}

	if len(violations) == 0 {
		return
	}
	t.Fatalf("envelope-only tools must use safecmd for path/confine/caps scaffolding:\n  %s", strings.Join(violations, "\n  "))
}
