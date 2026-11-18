package forbiddencalls

import (
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/test/contract/laws"
)

var security = []laws.Rule{{
	// Every protected settings file joined under the overlay directory is a
	// registered basename, so the floor covers it.
	Name:    "settings-overlay-basenames-are-registered",
	Anchors: []laws.Anchor{laws.Func("internal/settingsoverlay", "SettingsOverlayBasenames")},
	Check: func(r *laws.Run) {
		registered := settingsoverlay.SettingsOverlayBasenames()
		r.Expect(len(registered), "registered overlay basenames")
		dirs := []string{"internal/settings", "internal/mcp", "internal/llm", "internal/standingpatterns"}
		for _, f := range r.GoFiles(func(f *laws.GoFile) bool { return !f.IsTest && contains(dirs, f.Dir) }, dirs...) {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if laws.CalleeName(call) != "Join" || len(call.Args) < 3 {
					return
				}
				dir, _ := laws.StringLit(call.Args[len(call.Args)-2])
				base, ok := laws.StringLit(call.Args[len(call.Args)-1])
				if dir == settingsoverlay.DirName() && ok && !strings.Contains(base, "/") && !contains(registered, base) {
					r.ReportAt(call.Pos(), "joins %s/%s, which SettingsOverlayBasenames does not register", dir, base)
				}
			})
		}
	},
}, {
	// Capability preflight gates on Contract.Supports, never a tool name.
	Name:    "capability-preflight-never-gates-on-a-tool-name",
	Anchors: []laws.Anchor{laws.Package("internal/toolcontract")},
	Check: func(r *laws.Run) {
		files := r.GoFiles(func(f *laws.GoFile) bool {
			base := path.Base(f.Rel)
			return !f.IsTest && (strings.Contains(base, "capability") || strings.HasPrefix(base, "socket_"))
		}, "internal")
		for _, f := range files {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if branch, ok := n.(*ast.IfStmt); ok && branch.Else == nil && rejectsCapabilityBranch(branch.Body) {
					return false
				}
				cmp, ok := n.(*ast.BinaryExpr)
				if !ok || cmp.Op != token.EQL && cmp.Op != token.NEQ {
					return true
				}
				if id, ok := cmp.X.(*ast.Ident); ok && id.Name == "tool" {
					if lit, ok := laws.StringLit(cmp.Y); ok {
						r.ReportAt(cmp.Pos(), "gates capability preflight on the tool name %q", lit)
					}
				}
				return true
			})
		}
	},
}, {
	// Equivalence classes document a pack; the matcher, selection and rule
	// code never read them, so they cannot widen a match.
	Name: "detection-pack-matcher-ignores-equivalents",
	Anchors: []laws.Anchor{
		laws.Decl("internal/detectionpack", "Pack.Equivalents"),
		laws.GoPath("internal/detectionpack/matcher.go"),
		laws.GoPath("internal/detectionpack/selection.go"),
		laws.GoPath("internal/detectionpack/rule.go"),
	},
	Check: func(r *laws.Run) {
		for _, rel := range []string{"internal/detectionpack/matcher.go", "internal/detectionpack/selection.go", "internal/detectionpack/rule.go"} {
			f := r.GoFile(rel)
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "Equivalents" {
					r.ReportAt(id.Pos(), "reads Equivalents")
				}
				return true
			})
		}
	},
}, {
	// Command matching belongs to detection packs and the human rule matcher;
	// settings code adds no new command or argv pattern matching.
	Name:    "settings-adds-no-command-matchers",
	Anchors: []laws.Anchor{laws.Func("internal/settings", "MatchCommandPattern")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/settings") {
			f.Funcs(func(fn *ast.FuncDecl) {
				f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
					matcher := pkg == "strings" && contains([]string{"Contains", "HasPrefix", "ContainsAny", "HasSuffix"}, name) ||
						pkg == "regexp" && contains([]string{"MatchString", "Match", "Compile", "MustCompile"}, name)
					if matcher && len(call.Args) > 0 && argLooksLikeCommandOrArgv(call.Args[0]) {
						r.ReportAt(call.Pos(), "%s matches %s.%s on a command or argv", fn.Name.Name, pkg, name)
					}
				})
			})
		}
	},
}}

func rejectsCapabilityBranch(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 5 {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "rejectBeforeInvoke" {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok || receiver.Name != "e" {
		return false
	}
	rejection, ok := call.Args[4].(*ast.UnaryExpr)
	if !ok || rejection.Op != token.AND {
		return false
	}
	literal, ok := rejection.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	kind, ok := literal.Type.(*ast.Ident)
	return ok && kind.Name == "ToolReject"
}

func argLooksLikeCommandOrArgv(expr ast.Expr) bool {
	switch x := expr.(type) {
	case *ast.Ident:
		switch x.Name {
		case "command", "cmd", "normalized", "argv", "token", "tok", "arg", "args":
			return true
		}
	case *ast.SelectorExpr:
		if x.Sel != nil {
			switch x.Sel.Name {
			case "Command", "Argv", "Args":
				return true
			}
		}
	case *ast.CallExpr:
		// strings.Fields(command), strings.TrimSpace(command), etc.
		if len(x.Args) > 0 {
			return argLooksLikeCommandOrArgv(x.Args[0])
		}
	}
	return false
}

var approvalMint = []laws.Rule{{
	// A tool_approval checkpoint is minted on reviewed seams only, so one
	// gate decides every interruption.
	Name:    "approvals-have-one-mint-seam",
	Anchors: []laws.Anchor{laws.Decl("internal/hitl", "CheckpointRequest"), laws.Decl("pkg/api", "CheckpointKindToolApproval")},
	Exempt: []laws.Exemption{
		{Key: "internal/toolexecution/executor_tool_approval.go", Reason: "the executor's raise-and-wait seam"},
		{Key: "internal/session/sandbox_ask_broker.go", Reason: "the sandbox capability ask lifecycle the brokers share"},
		{Key: "internal/api/harness_approval.go", Reason: "the harness control plane raises a card for driven test stacks"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if pkg, name, ok := f.Qualified(lit.Type); !ok || pkg != "github.com/lycaon/lycaon/internal/hitl" || name != "CheckpointRequest" {
					return true
				}
				toolApproval := false
				f.Selectors(lit, func(_ *ast.SelectorExpr, pkg, name string) {
					toolApproval = toolApproval || pkg == "github.com/lycaon/lycaon/pkg/api" && name == "CheckpointKindToolApproval"
				})
				if toolApproval && !r.Exempt(f.Rel) {
					r.ReportAt(lit.Pos(), "mints a tool_approval checkpoint outside the raise seams")
				}
				return true
			})
		}
	},
}}

// packageManagerWords name package-manager environment or layouts; word
// boundaries keep identifiers such as "component" out.
var packageManagerWords = regexp.MustCompile(`(?i)(NPM_CONFIG|\bnpm\b|\bpnpm\b|\byarn\b|\bnpmrc\b|node_modules|\bcargo\b)`)

var runnerNeutrality = []laws.Rule{{
	// The command runner treats every program alike: no package-manager
	// environment or classifier, in code or in comments that would guide one.
	Name:    "exec-command-runner-has-no-package-manager-special-cases",
	Anchors: []laws.Anchor{laws.Package("internal/exec"), laws.Package("internal/hostcmd")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/exec", "internal/hostcmd") {
			for i, line := range strings.Split(string(f.Src), "\n") {
				if packageManagerWords.MatchString(line) {
					r.Report("%s:%d names a package manager: %s", f.Rel, i+1, strings.TrimSpace(line))
				}
			}
		}
	},
}, {
	// Sensitive-path classification resolves symlinks first, through
	// (*sensitivepath.Catalog).ClassifyResolved.
	Name:    "sensitive-path-classification-has-one-source",
	Anchors: []laws.Anchor{laws.Func("internal/sensitivepath", "Catalog.ClassifyResolved")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Match" && sel.Sel.Name != "MatchCovering" {
					return
				}
				if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "Locations" && !laws.Under(f, "internal/sensitivepath") {
					r.ReportAt(call.Pos(), "Catalog.%s bypasses symlink resolution; use ClassifyResolved", sel.Sel.Name)
				}
			})
		}
	},
}}

// trustChainHelperFlags are the helper flags the confined launch pipeline
// passes; the profile travels over the inherited descriptor.
var trustChainHelperFlags = []string{"__confine-exec", "--profile-fd", "--"}

var trustChain = []laws.Rule{{
	// The confined launch pipeline has three inputs. A profile read from the
	// environment, a staged file or an unknown flag would be a fourth.
	Name:    "trust-chain-has-closed-inputs",
	Anchors: []laws.Anchor{laws.Func("internal/confine", "Command"), laws.Func("internal/confine", "runHelper")},
	Check: func(r *laws.Run) {
		for _, name := range []string{"Command", "runHelper"} {
			fn := r.FuncDecl("internal/confine", name)
			f := r.Declaration("internal/confine", name).File
			f.Calls(fn.Body, func(call *ast.CallExpr, pkg, callee string) {
				if pkg != "os" {
					return
				}
				switch callee {
				case "Getenv", "LookupEnv":
					if literalArgMatches(call, mentionsProfile) {
						r.ReportAt(call.Pos(), "%s reads a profile from the environment", name)
					}
				case "Open", "OpenFile", "ReadFile", "Readlink":
					if literalArgMatches(call, mentionsProfile) || literalArgMatches(call, isStagingPath) {
						r.ReportAt(call.Pos(), "%s reads a profile from a file; pass it over the pipe", name)
					}
				}
			})
			laws.StringLits(fn.Body, func(lit *ast.BasicLit, value string) {
				if strings.HasPrefix(value, "--") && !contains(trustChainHelperFlags, value) &&
					(strings.Contains(value, "profile") || strings.Contains(value, "sandbox")) {
					r.ReportAt(lit.Pos(), "%s passes the unknown helper flag %s", name, value)
				}
				if isStagingPath(value) {
					r.ReportAt(lit.Pos(), "%s names a profile staging path", name)
				}
			})
		}
	},
}}

func literalArgMatches(call *ast.CallExpr, match func(string) bool) bool {
	for _, arg := range call.Args {
		if value, ok := laws.StringLit(arg); ok && match(value) {
			return true
		}
	}
	return false
}

func mentionsProfile(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "profile") || strings.Contains(lower, "sandbox") || strings.Contains(lower, "sbpl")
}

func isStagingPath(value string) bool {
	return strings.HasSuffix(value, ".sb") || strings.Contains(value, "lycaon-sbpl-")
}
