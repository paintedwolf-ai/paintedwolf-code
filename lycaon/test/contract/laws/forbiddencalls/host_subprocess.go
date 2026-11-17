package forbiddencalls

import (
	"go/ast"
	"path"

	"github.com/lycaon/lycaon/test/contract/laws"
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
func subjectForwarders(files []*laws.GoFile) map[string]int {
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
				if laws.CalleeName(call) != "HostLaunch" || len(call.Args) == 0 {
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

var hostSubprocess = []laws.Rule{{
	// Host subprocesses come from a closed, reasoned inventory, and none
	// shells out for an operation implemented natively.
	Name:    "host-subprocess-inventory",
	Anchors: []laws.Anchor{laws.Func("internal/exec", "HostLaunch")},
	Exempt:  approvedHostLaunches,
	Check: func(r *laws.Run) {
		g := r.Go()
		files := r.GoFiles(laws.Production, "internal", "cmd")
		subjectArg := subjectForwarders(files)
		for _, f := range files {
			if laws.Under(f, "internal/exec", "internal/confine") {
				continue
			}
			f.Calls(f.AST, func(call *ast.CallExpr, _, _ string) {
				if at, ok := subjectArg[laws.CalleeName(call)]; ok && at < len(call.Args) {
					if subject, ok := g.ConstString(f, call.Args[at]); ok {
						if reason, forbidden := forbiddenHostLaunchSubjects[subject]; forbidden {
							r.ReportAt(call.Pos(), "host subprocess %q: %s", subject, reason)
						} else if !r.Exempt(subject) {
							r.ReportAt(call.Pos(), "host subprocess %q is not in the inventory; use native Go or record why", subject)
						}
					}
				}
				for _, arg := range call.Args {
					if value, ok := laws.StringLit(arg); ok {
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
var approvedHostLaunches = []laws.Exemption{
	{Key: "generation_engine", Reason: "resident local E4B generation runtime with host-admitted device work"},
	{Key: "bundle_verification", Reason: "inspects Apple code signatures and notarization tickets on packaged app bundles"},
	{Key: "git_version_probe", Reason: "verifies host git binary meets minimum engine version requirement"},
	{Key: "decide_engine", Reason: "resident local decision engine (bialy) that ranks candidates and answers turn decisions"},
	{Key: "ssh_agent_socket_lookup", Reason: "retrieves SSH_AUTH_SOCK from launchd environment on headless macOS launches"},
	{Key: "test volume", Reason: "attaches disk images for placement tests on macOS; test support, never in the product"},
	{Key: "test volume namespace", Reason: "re-runs a placement test in a private mount namespace on Linux; test support, never in the product"},
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
