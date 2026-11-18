package forbiddencalls

import (
	"go/ast"
	"go/types"
	"slices"

	"github.com/lycaon/lycaon/test/contract/laws"
)

// agentAdapterPackages reach files on an agent's behalf through
// ProgramPaths, projectpaths and fseffect, which hold the admitted object.
var agentAdapterPackages = []string{"internal/tools/native", "internal/httpaction", "internal/commandprogram"}

// agentReachablePackages run on an agent's behalf.
var agentReachablePackages = append([]string{"internal/toolexecution", "internal/webresearch"}, agentAdapterPackages...)

// agentForbiddenRefs bypass the launch and network doors: a host launch or
// host Git purpose carries no agent confinement, os/exec starts a process
// outside the launch plan, and the default HTTP client dials without
// destination admission.
var agentForbiddenRefs = map[string][]string{
	"github.com/lycaon/lycaon/internal/exec":    {"HostLaunch"},
	"github.com/lycaon/lycaon/internal/gitexec": {"WithHostPurpose"},
	"os/exec":  {"Command", "CommandContext"},
	"net/http": {"Get", "Head", "Post", "PostForm", "NewRequest", "NewRequestWithContext", "DefaultClient", "DefaultTransport"},
	"net":      {"Dial", "DialTimeout", "DialTCP", "DialUDP", "DialUnix", "DialIP"},
}

// agentForbiddenBuilds reach the network without destination admission
// when an agent package builds its own.
var agentForbiddenBuilds = map[string][]string{
	"net/http": {"Client", "Transport"},
	"net":      {"Dialer"},
}

// rawFilesystemCalls reopen or change a pathname outside the door.
var rawFilesystemCalls = []string{
	"Open", "OpenFile", "ReadFile", "WriteFile", "Create", "CreateTemp", "Remove", "RemoveAll", "Rename",
	"Mkdir", "MkdirAll", "MkdirTemp", "Symlink", "Link", "Chmod", "Chown", "Lchown", "Chtimes", "Truncate", "ReadDir",
}

var agentDoors = []laws.Rule{{
	// Agent code launches through confined launch plans, reaches the network
	// through outboundhttp and holds credentials only through scoped services.
	Name: "agent-effects-through-their-doors",
	Anchors: []laws.Anchor{
		laws.Func("internal/exec", "HostLaunch"),
		laws.Func("internal/gitexec", "WithHostPurpose"),
		laws.Package("internal/outboundhttp"),
	},
	Exempt: []laws.Exemption{{
		Key:    "internal/webresearch/credentials.go",
		Reason: "reads the user's configured search provider key for the provider request the host makes",
	}},
	Check: func(r *laws.Run) {
		credentials := []string{"github.com/lycaon/lycaon/internal/gitcredential", "github.com/lycaon/lycaon/internal/credentialstore"}
		for _, f := range r.GoFiles(laws.Production, agentReachablePackages...) {
			var found []string
			for _, p := range credentials {
				if f.Imports(p) {
					found = append(found, "imports "+p)
				}
			}
			for p := range agentForbiddenRefs {
				if f.DotImports(p) {
					found = append(found, "dot-imports "+p)
				}
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if slices.Contains(agentForbiddenRefs[pkg], name) {
					found = append(found, "uses "+laws.SelectorName(sel))
				}
			})
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok {
					if pkg, name, ok := f.Qualified(lit.Type); ok && slices.Contains(agentForbiddenBuilds[pkg], name) {
						found = append(found, "builds "+laws.SelectorName(lit.Type))
					}
				}
				return true
			})
			if len(found) == 0 || r.Exempt(f.Rel) {
				continue
			}
			for _, use := range found {
				r.Report("%s %s: launch through confined launch plans, reach the network through outboundhttp, "+
					"use credentials only through scoped services", f.Rel, use)
			}
		}
	},
}, {
	// Agent adapters read and change files through ProgramPaths, projectpaths
	// or fseffect instead of reopening a pathname.
	Name:    "agent-adapters-reach-files-through-the-door",
	Anchors: []laws.Anchor{laws.Package("internal/fseffect"), laws.Package("internal/tools/projectpaths")},
	Exempt: []laws.Exemption{
		{Key: "internal/tools/native/command/command_not_found.go:resolvableNeighbours", Reason: "lists PATH directories for command-not-found suggestions; names only, never project content"},
		{Key: "internal/tools/native/gitcmd/remove.go:pruneEmptyParents", Reason: "checks emptiness of directories inside the bound repository before the delete door removes them"},
		{Key: "internal/tools/native/sourceview/rejects.go:PathNotFound", Reason: "lists the parent of a resolved missing path for spelling suggestions"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, agentAdapterPackages...) {
			if f.DotImports("os") {
				r.Report("%s dot-imports os", f.Rel)
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == "os" && slices.Contains(rawFilesystemCalls, name) && !r.Exempt(f.Site(sel.Pos())) {
					r.Report("%s uses os.%s: go through ProgramPaths, projectpaths or fseffect", f.Site(sel.Pos()), name)
				}
			})
		}
	},
}}

const extpacksPkg = "github.com/lycaon/lycaon/internal/extpacks"

// deviceCatalogBoundaries read the device catalog: the ambient task roster and
// the workflow manifest registry are device-scoped and have no session to
// resolve a turn view from.
var deviceCatalogBoundaries = []laws.Exemption{
	{Key: "internal/app/configuration/catalog.go", Reason: "device catalog composition"},
	{Key: "internal/app/build_server.go", Reason: "server composition"},
	{Key: "internal/app/workflows/build.go", Reason: "the device-scoped workflow registry"},
	{Key: "internal/app/execution/runtime.go", Reason: "the device-scoped execution runtime"},
	{Key: "internal/session/catalog/effective_catalog.go", Reason: "resolves a turn view from the device catalog"},
	{Key: "internal/session/profiles/effective_skills.go", Reason: "the ambient task roster"},
	{Key: "internal/session/catalog/catalog_view.go", Reason: "builds the turn view"},
	{Key: "internal/workflow/definition/manifest.go", Reason: "the workflow manifest registry"},
}

var catalogReach = []laws.Rule{{
	// Below the device boundaries, code reaches the catalog through a turn
	// View or WithCatalog.
	Name:    "extpacks-active-only-at-device-boundaries",
	Anchors: []laws.Anchor{laws.Func("internal/extpacks", "Active")},
	Exempt:  deviceCatalogBoundaries,
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config", "test") {
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == extpacksPkg && name == "Active" && !r.Exempt(f.Rel) {
					r.ReportAt(call.Pos(), "extpacks.Active() below the device boundaries; take a turn View")
				}
			})
		}
	},
}, {
	// A catalog or view is an explicit parameter, never a context value.
	Name:    "no-context-carried-catalog-or-view",
	Anchors: []laws.Anchor{laws.Decl("internal/extpacks", "EffectiveCatalog"), laws.Decl("internal/catalogview", "View")},
	Check: func(r *laws.Run) {
		for _, pkg := range r.TypedPackages(laws.Source...) {
			for _, f := range pkg.Files {
				f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
					fn := pkg.Callee(call)
					if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "context" || fn.Name() != "WithValue" || len(call.Args) != 3 {
						return
					}
					if carriesCatalog(pkg.Info.TypeOf(call.Args[2])) {
						r.ReportAt(call.Pos(), "context carries a catalog or view; pass it as a parameter")
					}
				})
			}
		}
	},
}}

func carriesCatalog(t types.Type) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	path, name := named.Obj().Pkg().Path(), named.Obj().Name()
	return path == extpacksPkg && name == "EffectiveCatalog" || path == "github.com/lycaon/lycaon/internal/catalogview" && name == "View"
}

// extensionHandlerBans are the subsystem's own steps; an extension handler
// submits one typed transaction intent and leaves them to it.
var extensionHandlerBans = []string{
	"PrepareInstall", "PrepareInstallMeta", "PrepareRemoval", "PrepareUpdate", "PrepareReload", "PrepareLockDesired",
	"ApplyMutationToState", "PrepareMetaMemberMutation", "EncodeDesired", "EncodeLock", "AcquireIntentLocks", "WithOmitted",
}

// packWalkPrimitives enumerate pack directories.
var packWalkPrimitives = []string{"KindDirs", "DiscoverEffective", "DiscoverStock", "DiscoverStockContent"}

var extensionTransactions = []laws.Rule{{
	Name:    "extension-handlers-submit-one-intent",
	Anchors: []laws.Anchor{laws.GoPath("internal/api/extensionadmin/extensions.go"), laws.Func("internal/extpacks", "PrepareInstall"), laws.Func("internal/extpacks", "AcquireIntentLocks")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/api/extensionadmin") {
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == extpacksPkg && contains(extensionHandlerBans, name) {
					r.ReportAt(sel.Pos(), "extpacks.%s; submit the intent and let the extension subsystem prepare and commit", name)
				}
			})
		}
	},
}, {
	// After resolve, loaders compile from the catalog's captured unit bytes,
	// so a catalog revision binds what they load.
	Name:    "pack-content-loaders-compile-from-catalog-bytes",
	Anchors: []laws.Anchor{laws.Func("internal/extpacks", "DiscoverStockContent"), laws.Func("internal/extpacks", "KindDirs")},
	Exempt: []laws.Exemption{
		{Key: "internal/prompts/bundled_layout.go", Reason: "reserved `_` template includes have no unit id"},
		{Key: "internal/prompts/persona_contract.go", Reason: "reserved persona-contract definition"},
		{Key: "internal/workflowvalidate/checks_closure.go", Reason: "author-ref existence probe"},
		{Key: "internal/testutil/extpackstest/catalog.go", Reason: "test catalog staging helper"},
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, laws.Source...) {
			if laws.Under(f, "internal/extpacks") {
				continue
			}
			walks := false
			f.Calls(f.AST, func(_ *ast.CallExpr, pkg, name string) {
				walks = walks || pkg == extpacksPkg && contains(packWalkPrimitives, name)
			})
			if walks && !r.Exempt(f.Rel) {
				r.Report("%s walks pack directories after resolve; compile from catalog.LoadedUnitIDs and UnitContent", f.Rel)
			}
		}
	},
}}

const (
	sandboxPkg      = "github.com/lycaon/lycaon/internal/sandbox"
	toolcontractPkg = "github.com/lycaon/lycaon/internal/toolcontract"
)

var reachMarks = []string{"ReachCommandMutation", "ReachCommandExternalExec", "ReachCommandNetworkEgress"}

var promptReader = []laws.Rule{{
	// In internal/prompts only DeclaredReader loads tool profiles from disk.
	Name: "prompts-load-tool-profiles-through-declared-reader",
	Anchors: []laws.Anchor{
		laws.Func("internal/prompts", "DeclaredReader"),
		laws.Func("internal/sandbox", "LoadToolProfiles"),
		laws.Func("internal/sandbox", "LoadToolProfilesWithCatalog"),
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal/prompts") {
			f.Funcs(func(fn *ast.FuncDecl) {
				if fn.Name.Name == "DeclaredReader" {
					return
				}
				f.Calls(fn.Body, func(call *ast.CallExpr, pkg, name string) {
					if pkg == sandboxPkg && (name == "LoadToolProfiles" || name == "LoadToolProfilesWithCatalog") {
						r.ReportAt(call.Pos(), "%s calls sandbox.%s; read through DeclaredReader", fn.Name.Name, name)
					}
				})
			})
		}
	},
}, {
	// Session and execution packages receive a reader; they never build one.
	Name:    "session-execution-never-calls-declared-reader",
	Anchors: []laws.Anchor{laws.Func("internal/prompts", "DeclaredReader")},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal/coordinator", "internal/session", "internal/app/sessions", "internal/toolexecution") {
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "DeclaredReader" {
					r.ReportAt(call.Pos(), "calls DeclaredReader")
				}
			})
		}
	},
}, {
	// Reach marks are produced only by Reader.Held.
	Name: "reach-marks-produced-by-reader-held",
	Anchors: []laws.Anchor{
		laws.Decl("internal/toolcontract", "ReachMarks"),
		laws.Decl("internal/toolcontract", "ReachCommandMutation"),
		laws.Decl("internal/toolcontract", "ReachCommandExternalExec"),
		laws.Decl("internal/toolcontract", "ReachCommandNetworkEgress"),
	},
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.AnyGo, "internal", "cmd", "pkg", "test") {
			if laws.Under(f, "internal/toolcontract", "internal/prompts") {
				continue
			}
			f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
				if pkg == toolcontractPkg && name == "ReachMarks" {
					r.ReportAt(sel.Pos(), "references toolcontract.ReachMarks")
				}
			})
			if f.IsTest {
				continue
			}
			ast.Inspect(f.AST, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range assign.Lhs {
					if idx, ok := lhs.(*ast.IndexExpr); ok && isReachMark(f, idx.Index) {
						r.ReportAt(lhs.Pos(), "inserts a reach mark into a map")
					}
				}
				return true
			})
		}
	},
}}

func isReachMark(f *laws.GoFile, expr ast.Expr) bool {
	if pkg, name, ok := f.Qualified(expr); ok {
		return pkg == toolcontractPkg && slices.Contains(reachMarks, name)
	}
	id, ok := expr.(*ast.Ident)
	return ok && slices.Contains(reachMarks, id.Name)
}
