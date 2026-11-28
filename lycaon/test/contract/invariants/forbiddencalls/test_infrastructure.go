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
			f.Selectors(f.Syntax(), func(sel *ast.SelectorExpr, pkg, name string) {
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
			f.Selectors(f.Syntax(), func(sel *ast.SelectorExpr, pkg, name string) {
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
			f.Calls(f.Syntax(), func(call *ast.CallExpr, pkg, name string) {
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
			f.Calls(f.Syntax(), func(call *ast.CallExpr, pkg, name string) {
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
}, {
	// Every test or fuzz entry point reaching a hoststate authority accessor
	// must open and release HoldAuthorityMemos with a verified cleanup pattern
	// (t.Cleanup/defer in tests or helpers, or explicit release after m.Run in
	// TestMain) covering the accessor statement, so an ambient hold from
	// another parallel test cannot mask a missing hold at the call site.
	Name: "hoststate-authority-tests-hold-memos",
	Anchors: []invariants.Anchor{
		invariants.Func("test/contract/internal/hoststate", "HoldAuthorityMemos"),
		invariants.Func("test/contract/internal/hoststate", "FreezeTurnAuthority"),
		invariants.Func("test/contract/internal/hoststate", "FreezeWorkerAuthority"),
	},
	Exempt: []invariants.Exemption{{
		Key:    "test/contract/internal/hoststate/authority_hold_test.go",
		Reason: "exercises unheld authority accessors to verify ErrAuthorityMemosNotHeld",
	}},
	Check: checkHostStateAuthorityHolds,
}}

const hoststateImportPath = "github.com/lycaon/lycaon/test/contract/internal/hoststate"

var hoststateAuthorityAccessors = map[string]bool{
	"BuildWorkerState":                 true,
	"ExecutorForOverlay":               true,
	"FreezeTurnAuthority":              true,
	"FreezeTurnAuthorityWithResources": true,
	"FreezeWorkerAuthority":            true,
	"RegistryAndExecutorForOverlay":    true,
	"ResolveWorkerSeed":                true,
	"WorkerReaderForSession":           true,
	"WorkerStates":                     true,
}

func checkHostStateAuthorityHolds(r *invariants.Run) {
	byPkg := map[string][]*invariants.GoFile{}
	for _, f := range r.GoFiles(invariants.Tests, "test/contract") {
		key := f.Dir + "\x00" + f.PkgName
		byPkg[key] = append(byPkg[key], f)
	}
	for _, files := range byPkg {
		importsHostState := false
		for _, f := range files {
			if f.Imports(hoststateImportPath) {
				importsHostState = true
				break
			}
		}
		if !importsHostState {
			continue
		}
		checkHostStateAuthorityHoldPkg(r, files)
	}
}

func checkHostStateAuthorityHoldPkg(r *invariants.Run, files []*invariants.GoFile) {
	funcPos := map[string]token.Pos{}
	funcRel := map[string]string{}
	unheldDirectAuth := map[string]bool{}
	unheldLocalCalls := map[string][]string{}
	testMainCoversRun := false

	for _, f := range files {
		f.Funcs(func(fn *ast.FuncDecl) {
			if fn.Recv != nil || fn.Body == nil {
				return
			}
			name := fn.Name.Name
			funcPos[name] = fn.Pos()
			funcRel[name] = f.Rel

			isTestMain := name == "TestMain"
			start, end, hasHold := verifiedHoldSpan(f, fn, isTestMain)
			if isTestMain && hasHold {
				testMainCoversRun = true
			}

			inspectStmt := func(stmt ast.Stmt) {
				f.Calls(stmt, func(_ *ast.CallExpr, pkg, callee string) {
					if pkg == hoststateImportPath && hoststateAuthorityAccessors[callee] {
						unheldDirectAuth[name] = true
					}
				})
				ast.Inspect(stmt, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok && id.Name != "" {
						unheldLocalCalls[name] = append(unheldLocalCalls[name], id.Name)
					}
					return true
				})
			}

			for i, stmt := range fn.Body.List {
				if hasHold && i >= start && i < end {
					continue
				}
				inspectStmt(stmt)
			}
		})
	}
	if testMainCoversRun {
		return
	}
	for name, pos := range funcPos {
		if name == "TestMain" || (!strings.HasPrefix(name, "Test") && !strings.HasPrefix(name, "Fuzz") && !strings.HasPrefix(name, "Benchmark")) {
			continue
		}
		if r.Exempt(funcRel[name]) {
			continue
		}
		visited := map[string]bool{name: true}
		queue := []string{name}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if unheldDirectAuth[cur] {
				if cur == name {
					r.ReportAt(pos, "%s calls hoststate authority accessor outside a verified HoldAuthorityMemos span", name)
				} else {
					r.ReportAt(pos, "%s reaches hoststate authority accessor via %s outside a verified HoldAuthorityMemos span", name, cur)
				}
				break
			}
			for _, next := range unheldLocalCalls[cur] {
				if visited[next] {
					continue
				}
				if _, exists := funcPos[next]; exists {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
}

// verifiedHoldSpan returns the statement-index range [start, end) in fn.Body.List
// covered by a verified HoldAuthorityMemos acquisition and cleanup pattern.
// Cleanup calls must target the enclosing function's testing.T/F/B/TB parameter,
// an explicit early release stops coverage even if a defer/Cleanup was also
// registered, and in TestMain only an explicit release after the *testing.M
// parameter's direct Run() call is accepted.
func verifiedHoldSpan(f *invariants.GoFile, fn *ast.FuncDecl, isTestMain bool) (int, int, bool) {
	stmts := fn.Body.List
	testParams := testingParamNames(f, fn)
	testMainM := ""
	if isTestMain {
		testMainM = testMainMParam(f, fn)
	}
	for i, stmt := range stmts {
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			if !isTestMain && isCleanupWithHoldCall(f, s.X, testParams) {
				return i, len(stmts), true
			}
		case *ast.DeferStmt:
			if !isTestMain && s.Call != nil && isHoldAuthorityCall(f, s.Call.Fun) {
				return i, len(stmts), true
			}
		case *ast.AssignStmt:
			if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
				continue
			}
			id, ok := ast.Unparen(s.Lhs[0]).(*ast.Ident)
			if !ok || id.Name == "_" || !isHoldAuthorityCall(f, s.Rhs[0]) {
				continue
			}
			if end, ok := findVerifiedRelease(stmts, i+1, id.Name, testParams, testMainM); ok {
				return i, end, true
			}
		}
	}
	return 0, 0, false
}

func testingParamNames(f *invariants.GoFile, fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	if fn.Type == nil || fn.Type.Params == nil {
		return out
	}
	for _, field := range fn.Type.Params.List {
		expr := ast.Unparen(field.Type)
		isPtr := false
		if star, ok := expr.(*ast.StarExpr); ok {
			isPtr = true
			expr = ast.Unparen(star.X)
		}
		pkg, name, ok := f.Qualified(expr)
		if !ok || pkg != "testing" {
			continue
		}
		valid := (!isPtr && name == "TB") || (isPtr && (name == "T" || name == "F" || name == "B"))
		if !valid {
			continue
		}
		for _, id := range field.Names {
			out[id.Name] = true
		}
	}
	return out
}

func testMainMParam(f *invariants.GoFile, fn *ast.FuncDecl) string {
	if fn.Type == nil || fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		star, ok := ast.Unparen(field.Type).(*ast.StarExpr)
		if !ok {
			continue
		}
		pkg, name, ok := f.Qualified(star.X)
		if ok && pkg == "testing" && name == "M" && len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

func isHoldAuthorityCall(f *invariants.GoFile, expr ast.Expr) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	pkg, name, ok := f.Qualified(call.Fun)
	return ok && pkg == hoststateImportPath && name == "HoldAuthorityMemos"
}

func isCleanupCallOn(call *ast.CallExpr, testParams map[string]bool) bool {
	if call == nil || len(call.Args) != 1 {
		return false
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Cleanup" {
		return false
	}
	recv, ok := ast.Unparen(sel.X).(*ast.Ident)
	return ok && testParams[recv.Name]
}

func isCleanupWithHoldCall(f *invariants.GoFile, expr ast.Expr, testParams map[string]bool) bool {
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	return ok && isCleanupCallOn(call, testParams) && isHoldAuthorityCall(f, call.Args[0])
}

func findVerifiedRelease(stmts []ast.Stmt, from int, varName string, testParams map[string]bool, testMainM string) (int, bool) {
	isTestMain := testMainM != ""
	sawMRun := false
	hasDeferredCleanup := false
	for j := from; j < len(stmts); j++ {
		if isTestMain && stmtCallsMRun(stmts[j], testMainM) {
			sawMRun = true
		}
		switch s := stmts[j].(type) {
		case *ast.DeferStmt:
			if !isTestMain && isZeroArgIdentCall(s.Call, varName) {
				hasDeferredCleanup = true
			}
		case *ast.ExprStmt:
			call, ok := ast.Unparen(s.X).(*ast.CallExpr)
			if !ok {
				continue
			}
			if !isTestMain && isCleanupCallOn(call, testParams) {
				if arg, ok := ast.Unparen(call.Args[0]).(*ast.Ident); ok && arg.Name == varName {
					hasDeferredCleanup = true
				}
			}
			if isZeroArgIdentCall(call, varName) {
				if isTestMain && !sawMRun {
					return 0, false
				}
				return j, true
			}
		}
	}
	if hasDeferredCleanup {
		return len(stmts), true
	}
	return 0, false
}

func isZeroArgIdentCall(call *ast.CallExpr, name string) bool {
	if call == nil || len(call.Args) != 0 {
		return false
	}
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	return ok && id.Name == name
}

func stmtCallsMRun(stmt ast.Stmt, mParam string) bool {
	if mParam == "" {
		return false
	}
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if _, isClosure := n.(*ast.FuncLit); isClosure {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			return true
		}
		if recv, ok := ast.Unparen(sel.X).(*ast.Ident); ok && recv.Name == mParam {
			found = true
			return false
		}
		return true
	})
	return found
}

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
	Check: func(r *invariants.Run) {
		banned := map[string]string{
			"github.com/lycaon/lycaon/internal/tools/projectpaths": "ResolveRead",
			"github.com/lycaon/lycaon/internal/confine":            "DefaultConfinement",
			"github.com/lycaon/lycaon/internal/tools/toolkit":      "PathEscapeReject",
		}
		for _, f := range r.GoFiles(isEnvelopeOnlyTool, "internal/tools/native/survey", "internal/tools/native/jq") {
			f.Selectors(f.Syntax(), func(sel *ast.SelectorExpr, pkg, name string) {
				if banned[pkg] == name {
					r.ReportAt(sel.Pos(), "%s; take it from safecmd", invariants.SelectorName(sel))
				}
			})
			for _, decl := range f.Syntax().Decls {
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
			f.Selectors(f.Syntax(), func(sel *ast.SelectorExpr, pkg, name string) {
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
			f.Selectors(f.Syntax(), func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "github.com/lycaon/lycaon/pkg/api" && (name == "ToolResultOutcomeError" || name == "ToolResultOutcomeRejected") {
					r.ReportAt(sel.Pos(), "states %s; return a coded refusal", name)
				}
			})
		}
	},
}}
