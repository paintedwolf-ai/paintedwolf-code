package forbiddencalls

import (
	"go/ast"
	"go/token"
	"path"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// testDirs hold every Go test of the module.
var testDirs = []string{"internal", "cmd", "pkg", "test"}

var testInfrastructure = []invariants.Invariant{{
	// Leak teardown and its ignore list are shared through one seam.
	Name:    "goleak-through-testutil",
	Anchors: []invariants.Anchor{invariants.Func("internal/testutil", "VerifyNoLeaks")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.AnyGo, testDirs...) {
			if invariants.Under(f, "internal/testutil") {
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
	Anchors: []invariants.Anchor{invariants.Dir("lycaon/internal/testsetup")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Tests, testDirs...) {
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
	Anchors: []invariants.Anchor{invariants.Func("internal/testsetup/git", "Enable"), invariants.Package("internal/testutil/gittest")},
	Check: func(r *invariants.Run) {
		const gittest = "github.com/lycaon/lycaon/internal/testutil/gittest"
		uses, enables := map[string]string{}, map[string]bool{}
		for _, f := range r.GoFiles(invariants.Tests, testDirs...) {
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
	Anchors: []invariants.Anchor{invariants.Func("internal/db", "Open"), invariants.Package("internal/testdbfixture")},
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
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Tests, testDirs...) {
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
	Anchors: []invariants.Anchor{
		invariants.Func("internal/configlayout", "FindModuleRoot"),
		invariants.Func("internal/testutil", "CheckoutRoot"),
		invariants.Package("internal/testutil/gittest"),
	},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.AnyGo, testDirs...) {
			f.Funcs(func(fn *ast.FuncDecl) {
				name := strings.ToLower(fn.Name.Name)
				switch {
				case f.IsTest && !strings.HasPrefix(fn.Name.Name, "Test") && strings.Contains(name, "moduleroot"):
					r.ReportAt(fn.Pos(), "%s; use configlayout.FindModuleRoot", fn.Name.Name)
				case f.IsTest && !strings.HasPrefix(fn.Name.Name, "Test") && strings.Contains(name, "reporoot"):
					r.ReportAt(fn.Pos(), "%s; use testutil.CheckoutRoot", fn.Name.Name)
				case fn.Name.Name == "initGitRepo" && !invariants.Under(f, "internal/git"):
					r.ReportAt(fn.Pos(), "initGitRepo; use internal/testutil/gittest")
				}
			})
			if invariants.Under(f, "internal/git", "internal/testutil/gittest") {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg != "os/exec" || (name != "Command" && name != "CommandContext") {
					return
				}
				hasGit, hasInit := false, false
				for _, arg := range call.Args {
					value, _ := invariants.StringLit(arg)
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

func directDatabaseScenarios(files ...string) []invariants.Exemption {
	out := make([]invariants.Exemption, 0, len(files))
	for _, file := range files {
		out = append(out, invariants.Exemption{Key: file, Reason: "the scenario opens a store on a crafted file to exercise recovery, backup or search"})
	}
	return out
}

// isEnvelopeOnlyTool selects the envelope-only analysis tools, find, grep
// and jq, which take path resolution, confinement and caps from safecmd.
func isEnvelopeOnlyTool(f *invariants.GoFile) bool {
	base := path.Base(f.Rel)
	return !f.IsTest && (invariants.Under(f, "internal/tools/native/jq") ||
		f.Dir == "internal/tools/native/survey" && (strings.HasPrefix(base, "find") || strings.HasPrefix(base, "grep")))
}

// capNameParts mark a host cap constant safecmd declares.
var capNameParts = []string{"Max", "Timeout", "Bytes", "Depth", "Scan", "Zoom", "Entries", "Paths", "Files", "Matches", "Results", "Pattern", "Context", "Recursive"}

var surveyTools = []invariants.Invariant{{
	Name: "envelope-only-tools-use-safecmd",
	Anchors: append(goPaths([]string{
		"internal/tools/native/survey/find.go",
		"internal/tools/native/survey/find_walk.go",
		"internal/tools/native/survey/grep.go",
		"internal/tools/native/jq/decode.go",
	}), invariants.Package("internal/tools/safecmd")),
	Exempt: []invariants.Exemption{{Key: "internal/tools/native/survey/grep.go:hostGrepMaxFileBytes", Reason: "a package variable tests lower to exercise the clamp"}},
	Check: func(r *invariants.Run) {
		banned := map[string]string{
			"github.com/lycaon/lycaon/internal/tools/projectpaths": "ResolveRead",
			"github.com/lycaon/lycaon/internal/confine":            "DefaultConfinement",
			"github.com/lycaon/lycaon/internal/tools/toolkit":      "PathEscapeReject",
		}
		for _, f := range r.GoFiles(isEnvelopeOnlyTool, "internal/tools/native/survey", "internal/tools/native/jq") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if banned[pkg] == name {
					r.ReportAt(sel.Pos(), "%s; take it from safecmd", invariants.SelectorName(sel))
				}
			})
			for _, decl := range f.AST.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
					continue
				}
				for _, spec := range gen.Specs {
					for _, name := range spec.(*ast.ValueSpec).Names {
						if !isHostCapName(name.Name) || (gen.Tok == token.VAR && r.Exempt(f.Rel+":"+name.Name)) {
							continue
						}
						r.ReportAt(name.Pos(), "redeclares %s; use safecmd", name.Name)
					}
				}
			}
		}
	},
}, {
	// Survey surfaces walk project trees only through sandbox.SurveyWalk,
	// the one place the prune policy applies.
	Name:    "survey-walks-use-the-shared-primitive",
	Anchors: []invariants.Anchor{invariants.Func("internal/sandbox", "SurveyWalk")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/tools/native", "internal/repomap", "internal/workspace", "internal/sandbox") {
			if f.Rel == "internal/sandbox/survey_walk.go" {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if name == "WalkDir" && (pkg == "path/filepath" || pkg == "io/fs") {
					r.ReportAt(sel.Pos(), "raw %s; walk through sandbox.SurveyWalk", invariants.SelectorName(sel))
				}
			})
		}
	},
}}

func isHostCapName(name string) bool {
	if !strings.HasPrefix(name, "host") {
		return false
	}
	for _, part := range capNameParts {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func goPaths(rels []string) []invariants.Anchor {
	out := make([]invariants.Anchor, 0, len(rels))
	for _, rel := range rels {
		out = append(out, invariants.GoPath(rel))
	}
	return out
}

var toolIngest = []invariants.Invariant{{
	// A tool reads a whole file only after bounding it: a stat of its size
	// or a spill to disk in the same function.
	Name:    "tool-file-ingest-is-size-bounded",
	Anchors: []invariants.Anchor{invariants.Package("internal/tools")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/tools") {
			f.Funcs(func(fn *ast.FuncDecl) {
				var slurps []*ast.CallExpr
				bounded := false
				f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
					callee := invariants.CalleeName(call)
					switch {
					case pkg == "os" && name == "ReadFile":
						slurps = append(slurps, call)
					case callee == "Size" || strings.HasSuffix(callee, "SpillBytes"):
						bounded = true
					}
				})
				for _, call := range slurps {
					if !bounded {
						r.ReportAt(call.Pos(), "%s reads a whole file without bounding its size", fn.Name.Name)
					}
				}
			})
		}
	},
}}

var toolOutcomes = []invariants.Invariant{{
	// Catalog tools report refusals through codes; the settled error or
	// rejected outcome is derived from them, never stated.
	Name:    "catalog-tools-state-no-uncoded-outcome",
	Anchors: []invariants.Anchor{invariants.Decl("pkg/api", "ToolResultOutcomeError"), invariants.Decl("pkg/api", "ToolResultOutcomeRejected")},
	Check: func(r *invariants.Run) {
		for _, f := range r.GoFiles(invariants.Production, "internal/tools") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "github.com/lycaon/lycaon/pkg/api" && (name == "ToolResultOutcomeError" || name == "ToolResultOutcomeRejected") {
					r.ReportAt(sel.Pos(), "states %s; return a coded refusal", name)
				}
			})
		}
	},
}}
