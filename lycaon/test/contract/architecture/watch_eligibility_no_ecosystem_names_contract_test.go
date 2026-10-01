package contract

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// ecosystemDirectoryNames must not influence project traversal.
var ecosystemDirectoryNames = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true,
	"dist": true, "build": true, "out": true, "target": true, "bin": true,
	"obj": true, ".next": true, ".nuxt": true, ".turbo": true, ".parcel-cache": true,
	".cache": true, "__pycache__": true, ".venv": true, "venv": true,
	"site-packages": true, ".gradle": true, ".tox": true, "Pods": true,
	"coverage": true, ".pytest_cache": true, ".mypy_cache": true,
}

// projectWalkDirs contains packages that decide project traversal.
var projectWalkDirs = []string{
	"internal/repochange",
	"internal/sourcefeed",
	"internal/session",
	"internal/sandbox",
	"internal/workspace",
	"internal/project",
}

// nonPathSubjects classifies ecosystem-shaped values that are not paths.
var nonPathSubjects = map[string]bool{
	// Source-symbol tag kind.
	"strings.TrimSpace(kind)": true,
}

// Empty scan roots would make the contract vacuous.
func TestProjectWalkDirsExist(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var missing []string
	for _, rel := range projectWalkDirs {
		info, err := os.Stat(filepath.Join(root, "lycaon", filepath.FromSlash(rel)))
		if err != nil || !info.IsDir() {
			missing = append(missing, rel)
		}
	}
	contractcheck.FailViolations(t, "projectWalkDirs names directories that no longer exist — the "+
		"ecosystem-name scan walks nothing there and passes vacuously", missing)
}

// Project-walk eligibility is decided by path facts, never by directory name.
// The only names these packages act on come from sandbox.ShouldSkipDir.
func TestWatchEligibilityNamesNoEcosystemDirectories(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")

	var hits []string
	corpus, err := contractcheck.LoadGoASTCorpus(lycaonRoot)
	contractcheck.FailErr(t, "load lycaon Go corpus", err)
	for _, relDir := range projectWalkDirs {
		prefix := filepath.ToSlash(relDir) + "/"
		for _, source := range corpus.Files() {
			if source.IsTest || !strings.HasPrefix(source.Rel, prefix) {
				continue
			}
			hits = append(hits, scanEcosystemNames(corpus.Fset, source.AST, source.Rel)...)
		}
	}

	sort.Strings(hits)
	if len(hits) > 0 {
		t.Fatalf(`project walk names ecosystem directories: %v

Prune with sandbox.ShouldSkipDir — VCS and engine-overlay metadata, by path
segment — and bound the rest by budget: the descriptor budget in internal/watchfd
with shallowest-first registration (repochange.watchOrder) for watchers, the
entry/depth caps for snapshots. Report what went uncovered through WatchCoverage
or a truncation banner; downstream, a skipped directory reads as "nothing
changed" rather than "not looked at".

If the literal is not a filesystem name, place its comparison subject in
nonPathSubjects with the reason.`, hits)
	}
}

// scanEcosystemNames reports ecosystem-named literals used as paths.
func scanEcosystemNames(fset *token.FileSet, f *ast.File, rel string) []string {
	subjects := ecosystemLiteralSubjects(fset, f)

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := ecosystemLiteral(n)
		if !ok {
			return true
		}
		subject, classified := subjects[lit.Pos()]
		if classified && nonPathSubjects[subject] {
			return true
		}
		where := rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
		if !classified {
			out = append(out, where+": "+lit.Value)
			return true
		}
		out = append(out, where+": "+lit.Value+" compared against "+subject)
		return true
	})
	return out
}

// ecosystemLiteralSubjects maps each ecosystem literal that reaches a comparison
// to the expression it is compared against.
func ecosystemLiteralSubjects(fset *token.FileSet, f *ast.File) map[token.Pos]string {
	out := map[token.Pos]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SwitchStmt:
			subject := renderSubject(fset, node.Tag)
			for _, stmt := range node.Body.List {
				clause, isClause := stmt.(*ast.CaseClause)
				if !isClause {
					continue
				}
				for _, expr := range clause.List {
					if lit, isLit := ecosystemLiteral(expr); isLit {
						out[lit.Pos()] = subject
					}
				}
			}
		case *ast.BinaryExpr:
			if node.Op != token.EQL && node.Op != token.NEQ {
				return true
			}
			for _, side := range [2]struct{ lit, other ast.Expr }{
				{node.X, node.Y}, {node.Y, node.X},
			} {
				if lit, isLit := ecosystemLiteral(side.lit); isLit {
					out[lit.Pos()] = renderSubject(fset, side.other)
				}
			}
		}
		return true
	})
	return out
}

func ecosystemLiteral(node ast.Node) (*ast.BasicLit, bool) {
	lit, ok := node.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil || !ecosystemDirectoryNames[value] {
		return nil, false
	}
	return lit, true
}

// renderSubject guards the nil tag of a bare `switch { … }`.
func renderSubject(fset *token.FileSet, node ast.Node) string {
	if node == nil {
		return ""
	}
	return renderNode(fset, node)
}
