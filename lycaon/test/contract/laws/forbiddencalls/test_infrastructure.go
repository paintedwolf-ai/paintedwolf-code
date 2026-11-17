package forbiddencalls

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// testDirs hold every Go test of the module.
var testDirs = []string{"internal", "cmd", "pkg", "test"}

var testInfrastructure = []laws.Rule{{
	// Leak teardown and its ignore list are shared through one seam.
	Name:    "goleak-through-testutil",
	Anchors: []laws.Anchor{laws.Func("internal/testutil", "VerifyNoLeaks")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, testDirs...) {
			if laws.Under(f, "internal/testutil") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "go.uber.org/goleak" {
					r.ReportAt(sel.Pos(), "goleak.%s outside testutil.VerifyNoLeaks", name)
				}
			})
		}
	},
}, {
	// Process-wide test setup lives in internal/testsetup.
	Name:    "test-main-uses-shared-setup",
	Anchors: []laws.Anchor{laws.Dir("lycaon/internal/testsetup")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Tests, testDirs...) {
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Name.Name != "TestMain" {
					return
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						switch sel.Sel.Name {
						case "LoadRegistryFromConfigRoot", "SetGuidanceRenderer", "TestingEnableBundledBinary":
							r.ReportAt(sel.Pos(), "TestMain repeats %s; route it through internal/testsetup", sel.Sel.Name)
						}
					}
					return true
				})
			})
		}
	},
}, {
	// A test binary that runs the pinned Git enables it, or its fixtures
	// resolve whatever Git the host has.
	Name:    "git-fixtures-enable-the-pinned-binary",
	Anchors: []laws.Anchor{laws.Func("internal/testsetup/git", "Enable"), laws.Package("internal/testutil/gittest")},
	Check: func(r *laws.Run) {
		const gittest = "github.com/lycaon/lycaon/internal/testutil/gittest"
		uses, enables := map[string]string{}, map[string]bool{}
		for _, f := range r.GoFiles(laws.Tests, testDirs...) {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				switch {
				case pkg == gittest && uses[f.Dir] == "":
					uses[f.Dir] = f.Rel
				case pkg == "github.com/lycaon/lycaon/internal/testsetup/git" && name == "Enable":
					enables[f.Dir] = true
				}
			})
		}
		for dir, file := range uses {
			if !enables[dir] {
				r.Report("%s uses Git fixtures without gittestsetup.Enable in the package TestMain", file)
			}
		}
	},
}, {
	// Disposable test databases come from testdbfixture.
	Name:    "disposable-databases-use-the-fixture",
	Anchors: []laws.Anchor{laws.Func("internal/db", "Open"), laws.Package("internal/testdbfixture")},
	Exempt: directDatabaseScenarios(
		"internal/api/recovery_snapshot_baseline_test.go",
		"internal/app/recovery_boot_test.go",
		"internal/app/recovery_failed_restore_test.go",
		"internal/app/recovery_startup_protocol_test.go",
		"internal/app/persistence/upgrade_recovery_test.go",
		"internal/backup/backup_test.go",
		"internal/backup/baseline_shape_test.go",
		"internal/backup/fresh_start_test.go",
		"internal/backup/restorable_test.go",
		"internal/backup/restore_transaction_test.go",
		"internal/db/search_global_scope_test.go",
		"internal/db/search_projection_test.go",
		"internal/db/search_sync_probe_test.go",
		"internal/project/promotion_engine_test.go",
	),
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Tests, testDirs...) {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == "github.com/lycaon/lycaon/internal/db" && name == "Open" && len(call.Args) > 0 && !r.Exempt(f.Rel) {
					r.ReportAt(call.Pos(), "direct db.Open; use testdbfixture")
				}
			})
		}
	},
}, {
	// Module roots, checkout roots and repository setup have one helper each.
	Name: "repository-fixtures-stay-centralized",
	Anchors: []laws.Anchor{
		laws.Func("internal/configlayout", "FindModuleRoot"),
		laws.Func("internal/testutil", "CheckoutRoot"),
		laws.Package("internal/testutil/gittest"),
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, testDirs...) {
			f.Funcs(func(fn *ast.FuncDecl) {
				name := strings.ToLower(fn.Name.Name)
				switch {
				case f.IsTest && !strings.HasPrefix(fn.Name.Name, "Test") && strings.Contains(name, "moduleroot"):
					r.ReportAt(fn.Pos(), "%s; use configlayout.FindModuleRoot", fn.Name.Name)
				case f.IsTest && !strings.HasPrefix(fn.Name.Name, "Test") && strings.Contains(name, "reporoot"):
					r.ReportAt(fn.Pos(), "%s; use testutil.CheckoutRoot", fn.Name.Name)
				case fn.Name.Name == "initGitRepo" && !laws.Under(f, "internal/git"):
					r.ReportAt(fn.Pos(), "initGitRepo; use internal/testutil/gittest")
				}
			})
			if laws.Under(f, "internal/git", "internal/testutil/gittest") {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg != "os/exec" || (name != "Command" && name != "CommandContext") {
					return
				}
				hasGit, hasInit := false, false
				for _, arg := range call.Args {
					value, _ := laws.StringLit(arg)
					hasGit = hasGit || value == "git"
					hasInit = hasInit || value == "init"
				}
				if hasGit && hasInit {
					r.ReportAt(call.Pos(), "runs git init; use internal/testutil/gittest")
				}
			})
		}
	},
}}

func directDatabaseScenarios(files ...string) []laws.Exemption {
	out := make([]laws.Exemption, 0, len(files))
	for _, file := range files {
		out = append(out, laws.Exemption{Key: file, Reason: "the scenario opens a store on a crafted file to exercise recovery, backup or search"})
	}
	return out
}
