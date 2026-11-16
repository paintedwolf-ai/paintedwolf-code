package contract

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// I2.2: No non-test function in internal/prompts other than DeclaredReader calls
// sandbox.LoadToolProfiles or sandbox.LoadToolProfilesWithCatalog.
func TestPromptsOnlyDeclaredReaderLoadsToolProfiles(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "prompts"))
	contractcheck.FailErr(t, "load prompts AST corpus", err)

	var offences []string
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		rel := filepath.ToSlash(gf.Rel)
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "DeclaredReader" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgIdent, ok := sel.X.(*ast.Ident)
				if !ok || pkgIdent.Name != "sandbox" {
					return true
				}
				switch sel.Sel.Name {
				case "LoadToolProfiles", "LoadToolProfilesWithCatalog":
					offences = append(offences, fmt.Sprintf("%s:%s calls sandbox.%s", rel, fn.Name.Name, sel.Sel.Name))
				}
				return true
			})
		}
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("I2.2 violated: internal/prompts functions outside DeclaredReader load tool profiles from disk:\n  %s",
			strings.Join(offences, "\n  "))
	}
}

// I2.3: DeclaredReader has no caller under internal/coordinator,
// internal/session, internal/app/sessions, or internal/toolexecution.
func TestSessionExecutionPackagesNeverCallDeclaredReader(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal"))
	contractcheck.FailErr(t, "load internal AST corpus", err)

	forbiddenPrefixes := []string{
		"coordinator/",
		"session/",
		"app/sessions/",
		"toolexecution/",
	}

	var offences []string
	for _, gf := range corp.Files() {
		rel := filepath.ToSlash(gf.Rel)
		var matched bool
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(rel, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "DeclaredReader" {
				offences = append(offences, rel)
			}
			return true
		})
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("I2.3 violated: session/execution packages call DeclaredReader:\n  %s",
			strings.Join(offences, "\n  "))
	}
}

var reachMarkIdents = map[string]bool{
	"ReachCommandMutation":      true,
	"ReachCommandExternalExec":  true,
	"ReachCommandNetworkEgress": true,
}

// I4.2: Nothing outside internal/toolcontract and internal/prompts references
// toolcontract.ReachMarks or inserts a reach mark into a map.
func TestReachMarksOnlyProducedByReaderHeld(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	var offences []string
	for _, gf := range corp.Files() {
		rel := filepath.ToSlash(gf.Rel)
		inAllowedPkg := strings.HasPrefix(rel, "internal/toolcontract/") || strings.HasPrefix(rel, "internal/prompts/")
		if inAllowedPkg {
			continue
		}
		ast.Inspect(gf.AST, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == "toolcontract" && node.Sel.Name == "ReachMarks" {
					offences = append(offences, fmt.Sprintf("%s references toolcontract.ReachMarks", rel))
				}
			case *ast.AssignStmt:
				if gf.IsTest {
					return true
				}
				for _, lhs := range node.Lhs {
					idx, ok := lhs.(*ast.IndexExpr)
					if ok && isReachMarkExpr(idx.Index) {
						offences = append(offences, fmt.Sprintf("%s inserts reach mark into map", rel))
					}
				}
			}
			return true
		})
	}
	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("I4.2 violated: reach marks produced outside Reader.Held:\n  %s",
			strings.Join(offences, "\n  "))
	}
}

func isReachMarkExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return reachMarkIdents[e.Name]
	case *ast.SelectorExpr:
		return reachMarkIdents[e.Sel.Name]
	}
	return false
}
