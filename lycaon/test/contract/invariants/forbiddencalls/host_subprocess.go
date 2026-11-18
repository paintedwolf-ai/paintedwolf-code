package forbiddencalls

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"path"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/invariants"
)

// forbiddenHostLaunchSubjects are host operations implemented natively.
var forbiddenHostLaunchSubjects = map[string]string{
	"move selected file to operating system trash": "use the native desktoptrash package instead of osascript, gio or powershell",
	"browser_quarantine_clear":                     "use native unix.Removexattr instead of the xattr CLI",
	"macOS idle-sleep assertion":                   "use the native IOKit IOPMAssertion instead of caffeinate",
	"account_shell_lookup":                         "use native getpwuid_r instead of dscacheutil",
	"loopback_listener_probe":                      "use the native lineage.Listeners socket tables instead of lsof",
}

// forbiddenCommandBinaries are CLIs whose host operations have native code.
var forbiddenCommandBinaries = map[string]string{
	"caffeinate":     "use the native hostpower IOKit power assertion",
	"osascript":      "use the native desktoptrash Foundation binding",
	"gio":            "use the native desktoptrash FreeDesktop implementation",
	"powershell":     "use the native desktoptrash Win32 SHFileOperationW",
	"powershell.exe": "use the native desktoptrash Win32 SHFileOperationW",
	"dscacheutil":    "use the native userpath getpwuid_r",
	"xattr":          "use native unix.Removexattr",
}

// subjectForwarders maps HostLaunch, and every function that passes one of
// its parameters straight through as HostLaunch's subject, to the argument
// position carrying the subject.
func subjectForwarders(files []*invariants.GoFile) map[string]int {
	out := map[string]int{"HostLaunch": 0}
	for _, f := range files {
		f.Funcs(func(fn *ast.FuncDecl) {
			params := map[string]int{}
			i := 0
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					params[name.Name] = i
					i++
				}
				if len(field.Names) == 0 {
					i++
				}
			}
			f.Calls(fn.Body, func(call *ast.CallExpr, _, _ string) {
				if invariants.CalleeName(call) != "HostLaunch" || len(call.Args) == 0 {
					return
				}
				if id, ok := call.Args[0].(*ast.Ident); ok {
					if at, isParam := params[id.Name]; isParam {
						out[fn.Name.Name] = at
					}
				}
			})
		})
	}
	return out
}

var hostSubprocess = []invariants.Invariant{{
	// Host subprocesses come from a closed, reasoned inventory, and none
	// shells out for an operation implemented natively.
	Name:    "host-subprocess-inventory",
	Anchors: []invariants.Anchor{invariants.Func("internal/exec", "HostLaunch")},
	Exempt:  approvedHostLaunches,
	Check: func(r *invariants.Run) {
		g := r.Go()
		files := r.GoFiles(invariants.Production, "internal", "cmd")
		subjectArg := subjectForwarders(files)
		for _, f := range files {
			if invariants.Under(f, "internal/exec", "internal/confine") {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if at, ok := subjectArg[invariants.CalleeName(call)]; ok && at < len(call.Args) {
					if subject, ok := g.ConstString(f, call.Args[at]); ok {
						if reason, forbidden := forbiddenHostLaunchSubjects[subject]; forbidden {
							r.ReportAt(call.Pos(), "host subprocess %q: %s", subject, reason)
						} else if !r.Exempt(subject) {
							r.ReportAt(call.Pos(), "host subprocess %q is not in the inventory; use native Go or record why", subject)
						}
					}
				}
				for _, arg := range call.Args {
					if value, ok := invariants.StringLit(arg); ok {
						if reason, forbidden := forbiddenCommandBinaries[path.Base(value)]; forbidden {
							r.ReportAt(call.Pos(), "invokes %q: %s", value, reason)
						}
					}
				}
			})
		}
	},
}}

// approvedHostLaunches is the closed inventory of host subprocess subjects.
var approvedHostLaunches = []invariants.Exemption{
	{Key: "generation_engine", Reason: "resident local E4B generation runtime with host-admitted device work"},
	{Key: "bundle_verification", Reason: "inspects Apple code signatures and notarization tickets on packaged app bundles"},
	{Key: "git_version_probe", Reason: "verifies host git binary meets minimum engine version requirement"},
	{Key: "decide_engine", Reason: "resident local decision engine (bialy) that ranks candidates and answers turn decisions"},
	{Key: "ssh_agent_socket_lookup", Reason: "retrieves SSH_AUTH_SOCK from launchd environment on headless macOS launches"},
	{Key: "test volume", Reason: "attaches disk images for placement tests on macOS; test support, never in the product"},
	{Key: "test volume namespace", Reason: "re-runs a placement test in a private mount namespace on Linux; test support, never in the product"},
	{Key: "source outage volume", Reason: "mounts and detaches the loopback NFS volume of source-outage tests on macOS; test support, never in the product"},
	{Key: "git", Reason: "executes Git CLI for worktree isolation, staging, signing, and repository management"},
	{Key: "git_config_query", Reason: "reads global and system git configuration cascade"},
	{Key: "git_identity_query", Reason: "queries author identity configured in host git"},
	{Key: "git_remote_query", Reason: "queries remote repository references"},
	{Key: "git_repo_config_audit", Reason: "audits repository-local git config for safety violations"},
	{Key: "log_config_editor", Reason: "spawns user interactive terminal editor ($EDITOR) for log configuration"},
	{Key: "logview_pipe", Reason: "executes user-authored shell pipeline in the TUI log viewer"},
	{Key: "document_extract_worker", Reason: "spawns memory-isolated child process for PDF/DOCX document extraction"},
	{Key: "release attestation verification", Reason: "runs GitHub CLI attestation verifier for signed releases"},
	{Key: "library scanner worker", Reason: "resident tree-sitter AST worker process for crash and leak containment"},
	{Key: "user_path_probe", Reason: "probes interactive login shell to recover user PATH on GUI launch"},
	{Key: "user_account_shell", Reason: "resolves the Linux account shell through configured NSS providers with bounded getent execution"},
	{Key: "toolchain_version_probe", Reason: "probes installed compilers and runtimes for version detection"},
	{Key: "fuse_mount", Reason: "mounts a worker view through fusermount3, the only unprivileged way to obtain a FUSE connection"},
	{Key: "fuse_unmount", Reason: "detaches a worker view through fusermount3, the only unprivileged way to remove a FUSE mount"},
	{Key: "fuse_probe", Reason: "proves a process outside the engine sees a probe mount coherently before selection trusts it"},
	{Key: "nfs_mount", Reason: "mounts a worker view through the system's mount_nfs, the only way to attach the built-in NFS client"},
	{Key: "nfs_unmount", Reason: "detaches a worker view through the system's umount and diskutil, the only way to remove a mount without privilege"},
	{Key: "fuse_barrier_flush", Reason: "re-executes the engine to flush a view's mount from a process that is not serving it, so the flush cannot deadlock on its server"},
	{Key: "nfs_barrier_flush", Reason: "re-executes the engine to flush a view's mount from a process that is not serving it, so the flush cannot deadlock on its server"},
	{Key: "nfs_probe", Reason: "proves a process outside the engine sees a probe mount coherently before selection trusts it"},
	{Key: "nix_generation_retention", Reason: "registers the running Nix store closure as an indirect GC root via the package-owned nix-store binary"},
}

// platformAdapters own platform differences under internal/ and report them
// as typed facts, unsupported included.
var platformAdapters = []string{"confine", "exec", "fseffect", "fspath", "hostos", "hostprocess", "lineage", "osprocess"}

// platformConsumers consume adapter facts and never branch on the platform.
var platformConsumers = []string{"commandsurface", "commandprogram", "tools", "toolhost"}

var (
	platformOSNames = []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux",
		"netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos", "unix", "other",
	}
	platformArchNames = []string{
		"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle",
		"ppc64", "ppc64le", "riscv64", "s390x", "wasm",
	}
)

var platform = []invariants.Invariant{{
	// The command environment's platform code lives in the declared adapters.
	Name:    "command-environment-platform-code-lives-in-adapters",
	Anchors: platformDirAnchors(platformConsumers),
	Check: func(r *invariants.Run) {
		for _, finding := range platformFindings(r, platformConsumers) {
			r.Report("%s: platform difference outside the adapters %s", finding, strings.Join(platformAdapters, ", "))
		}
	},
}, {
	// Every declared adapter exists outside the consumers and carries
	// platform code, so declaring a consumer cannot satisfy the rule.
	Name:    "platform-adapter-declaration-is-current",
	Anchors: platformDirAnchors(platformAdapters),
	Check: func(r *invariants.Run) {
		for _, adapter := range platformAdapters {
			for _, consumer := range platformConsumers {
				if adapter == consumer || strings.HasPrefix(adapter, consumer+"/") {
					r.Report("%s: declared adapter lies inside consumer %s", adapter, consumer)
				}
			}
			if len(platformFindings(r, []string{adapter})) == 0 {
				r.Report("%s: declared adapter carries no platform code", adapter)
			}
		}
	},
}}

func platformDirAnchors(dirs []string) []invariants.Anchor {
	out := make([]invariants.Anchor, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, invariants.Dir("lycaon/internal/"+dir))
	}
	return out
}

// platformFindings lists platform-suffixed or platform-constrained files and
// runtime.GOOS or GOARCH checks below internal/<root>.
func platformFindings(r *invariants.Run, roots []string) []string {
	dirs := make([]string, 0, len(roots))
	for _, root := range roots {
		dirs = append(dirs, "internal/"+root)
	}
	var findings []string
	for _, f := range r.GoFiles(invariants.Production, dirs...) {
		if platformFileSuffix(path.Base(f.Rel)) {
			findings = append(findings, f.Rel+": platform-suffixed file")
		}
		if platformBuildConstraint(f.AST) {
			findings = append(findings, f.Rel+": platform build constraint")
		}
		f.Selectors(f.AST, func(sel *ast.SelectorExpr, pkg, name string) {
			if pkg == "runtime" && (name == "GOOS" || name == "GOARCH") {
				findings = append(findings, fmt.Sprintf("%s: runtime.%s check", r.Go().Position(sel.Pos()), name))
			}
		})
	}
	return findings
}

func platformFileSuffix(name string) bool {
	parts := strings.Split(strings.TrimSuffix(name, ".go"), "_")
	if len(parts) < 2 {
		return false
	}
	last := parts[len(parts)-1]
	return slices.Contains(platformOSNames, last) || slices.Contains(platformArchNames, last)
}

func platformBuildConstraint(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil || platformConstraintTag(expr) {
				return true
			}
		}
	}
	return false
}

func platformConstraintTag(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return slices.Contains(platformOSNames, e.Tag) || slices.Contains(platformArchNames, e.Tag)
	case *constraint.NotExpr:
		return platformConstraintTag(e.X)
	case *constraint.AndExpr:
		return platformConstraintTag(e.X) || platformConstraintTag(e.Y)
	case *constraint.OrExpr:
		return platformConstraintTag(e.X) || platformConstraintTag(e.Y)
	}
	return false
}
