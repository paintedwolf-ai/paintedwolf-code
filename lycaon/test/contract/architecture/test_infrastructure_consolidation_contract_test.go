package contract

import (
	"go/ast"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestContractCorpusLoadersRejectEmptySelection(t *testing.T) {
	err := contractcheck.WalkFiles(t.TempDir(), map[string]struct{}{`.go`: {}}, false, func(string, []byte) error {
		return nil
	})
	if err == nil {
		t.Fatal("empty source corpus selection was accepted")
	}
	if _, err := contractcheck.LoadGoASTCorpus(t.TempDir()); err == nil {
		t.Fatal("empty Go corpus selection was accepted")
	}
}

func TestTestMainUsesSharedProcessSetup(t *testing.T) {
	t.Parallel()
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(contractcheck.RepoRoot(t), "lycaon"))
	contractcheck.FailErr(t, "load Go test corpus", err)
	var findings []string
	for _, source := range corpus.Files() {
		if !source.IsTest {
			continue
		}
		for _, decl := range source.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "LoadRegistryFromConfigRoot", "SetGuidanceRenderer", "TestingEnableBundledBinary":
					pos := corpus.Fset.Position(selector.Pos())
					findings = append(findings, pos.String()+": TestMain repeats shared process setup")
				}
				return true
			})
		}
	}
	contractcheck.FailViolations(t, "TestMain setup must route through internal/testsetup", findings)
}

// A test binary that runs the pinned Git must enable it, or its fixtures
// resolve whatever Git the host happens to have.
func TestGitFixturesEnableThePinnedBinary(t *testing.T) {
	t.Parallel()
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(contractcheck.RepoRoot(t), "lycaon"))
	contractcheck.FailErr(t, "load Go test corpus", err)
	uses, enables := map[string]string{}, map[string]bool{}
	for _, source := range corpus.Files() {
		if !source.IsTest {
			continue
		}
		dir := filepath.Dir(source.Rel)
		ast.Inspect(source.AST, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkg.Name == "gittest" && uses[dir] == "":
				uses[dir] = source.Rel
			case pkg.Name == "gittestsetup" && selector.Sel.Name == "Enable":
				enables[dir] = true
			}
			return true
		})
	}
	var findings []string
	for dir, file := range uses {
		if !enables[dir] {
			findings = append(findings, file+": Git fixtures without gittestsetup.Enable in the package TestMain")
		}
	}
	contractcheck.FailViolations(t, "Git test fixtures must enable the pinned binary", findings)
}

func TestDisposableDatabaseSetupUsesFixture(t *testing.T) {
	t.Parallel()
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(contractcheck.RepoRoot(t), "lycaon"))
	contractcheck.FailErr(t, "load Go test corpus", err)
	directOpenScenarios := map[string]bool{
		"internal/api/recovery_snapshot_baseline_test.go": true,
		"internal/app/recovery_boot_test.go":              true,
		"internal/app/recovery_failed_restore_test.go":    true,
		"internal/app/recovery_startup_protocol_test.go":  true,
		"internal/app/upgrade_recovery_snapshot_test.go":  true,
		"internal/backup/backup_test.go":                  true,
		"internal/backup/baseline_shape_test.go":          true,
		"internal/backup/fresh_start_test.go":             true,
		"internal/backup/restorable_test.go":              true,
		"internal/backup/restore_transaction_test.go":     true,
		"internal/db/search_global_scope_test.go":         true,
		"internal/db/search_projection_test.go":           true,
		"internal/db/search_sync_probe_test.go":           true,
		"internal/project/promotion_engine_test.go":       true,
	}
	var findings []string
	for _, source := range corpus.Files() {
		if !source.IsTest || directOpenScenarios[source.Rel] {
			continue
		}
		dbAliases := importedNames(source.AST, "github.com/lycaon/lycaon/internal/db")
		if len(dbAliases) == 0 {
			continue
		}
		ast.Inspect(source.AST, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 || !isPackageCall(call.Fun, dbAliases, "Open") {
				return true
			}
			pos := corpus.Fset.Position(call.Pos())
			findings = append(findings, pos.String()+": direct db.Open must use testdbfixture")
			return true
		})
	}
	contractcheck.FailViolations(t, "disposable databases must use internal/testdbfixture", findings)
}

func TestCommonRepositoryFixturesStayCentralized(t *testing.T) {
	t.Parallel()
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(contractcheck.RepoRoot(t), "lycaon"))
	contractcheck.FailErr(t, "load Go test corpus", err)
	var findings []string
	for _, source := range corpus.Files() {
		for _, decl := range source.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if source.IsTest && !strings.HasPrefix(fn.Name.Name, "Test") && strings.Contains(strings.ToLower(fn.Name.Name), "moduleroot") {
				pos := corpus.Fset.Position(fn.Pos())
				findings = append(findings, pos.String()+": use configlayout.FindModuleRoot")
			}
			if source.IsTest && !strings.HasPrefix(fn.Name.Name, "Test") && strings.Contains(strings.ToLower(fn.Name.Name), "reporoot") {
				pos := corpus.Fset.Position(fn.Pos())
				findings = append(findings, pos.String()+": use testutil.CheckoutRoot")
			}
			if fn.Name.Name == "initGitRepo" && !strings.HasPrefix(source.Rel, "internal/git/") {
				pos := corpus.Fset.Position(fn.Pos())
				findings = append(findings, pos.String()+": use internal/testutil/gittest")
			}
		}
		if strings.HasPrefix(source.Rel, "internal/git/") || strings.HasPrefix(source.Rel, "internal/testutil/gittest/") {
			continue
		}
		execAliases := importedNames(source.AST, "os/exec")
		ast.Inspect(source.AST, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			if !isPackageCall(call.Fun, execAliases, "Command") && !isPackageCall(call.Fun, execAliases, "CommandContext") {
				return true
			}
			hasGit, hasInit := false, false
			for _, arg := range call.Args {
				literal, ok := arg.(*ast.BasicLit)
				if !ok {
					continue
				}
				value, err := strconv.Unquote(literal.Value)
				if err == nil {
					hasGit = hasGit || value == "git"
					hasInit = hasInit || value == "init"
				}
			}
			if hasGit && hasInit {
				pos := corpus.Fset.Position(call.Pos())
				findings = append(findings, pos.String()+": use internal/testutil/gittest for repository setup")
			}
			return true
		})
	}
	contractcheck.FailViolations(t, "common repository fixtures must stay centralized", findings)
}

func importedNames(file *ast.File, importPath string) map[string]bool {
	names := map[string]bool{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		name := "db"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = true
	}
	return names
}

func isPackageCall(expr ast.Expr, packageNames map[string]bool, method string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != method {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && packageNames[ident.Name]
}
