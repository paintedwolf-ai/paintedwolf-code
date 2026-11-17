package forbiddencalls

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"github.com/lycaon/lycaon/test/contract/laws"
)

var writeDoor = []laws.Rule{{
	// Durable writes go through internal/fseffect, which stages a whole
	// replacement and commits it atomically.
	Name:    "durable-writes-go-through-one-door",
	Anchors: []laws.Anchor{laws.Package("internal/fseffect")},
	Exempt:  doorExemptions,
	Check: func(r *laws.Run) {
		for _, f := range r.GoFiles(laws.Production, "internal", "cmd", "pkg", "config") {
			if isDoorTestSupport(f) || laws.Under(f, "internal/fseffect") {
				continue
			}
			var found []*ast.CallExpr
			f.Calls(f.AST, func(call *ast.CallExpr, pkg, name string) {
				if pkg == "os" && (name == "Rename" || name == "WriteFile" || name == "Create" || name == "OpenFile" && openFileWrites(f, call)) {
					found = append(found, call)
				}
			})
			if len(found) == 0 || r.Exempt(f.Rel) {
				continue
			}
			for _, call := range found {
				r.ReportAt(call.Pos(), "durable write outside internal/fseffect; route it through the door or record the property the door cannot express")
			}
		}
	},
}}

// openFileWrites reports OpenFile flags that permit a write.
func openFileWrites(f *laws.GoFile, call *ast.CallExpr) bool {
	if len(call.Args) < 2 {
		return false
	}
	writes := false
	f.Selectors(call.Args[1], func(_ *ast.SelectorExpr, pkg, name string) {
		writes = writes || pkg == "os" && contains([]string{"O_WRONLY", "O_RDWR", "O_CREATE", "O_TRUNC", "O_APPEND"}, name)
	})
	return writes
}

// isDoorTestSupport identifies fixture writers whose scratch trees are discarded.
func isDoorTestSupport(f *laws.GoFile) bool {
	return strings.HasPrefix(f.Rel, "cmd/codegen-") || strings.HasSuffix(f.Rel, "_testsupport.go") ||
		laws.Under(f, "internal/testutil", "internal/api/contractfixture", "internal/testbaseline")
}

// doorExemptions are writes whose lifetime lies outside a replacement transaction,
// each with the property the door cannot express.
var doorExemptions = []laws.Exemption{

	{Key: "internal/fileclone/probe.go", Reason: "O_EXCL volume-capability probe in a private temporary directory; its fsynced input and clone are removed before return, never published as durable content"},
	{Key: "internal/scan/installed/verify.go", Reason: "ephemeral installed-scanner fixtures in an owned temporary directory removed before return"},
	// Content-addressed stores determine the destination after streaming.
	{Key: "internal/sourceblob/store.go", Reason: "stream-then-name: the object's destination is its digest"},

	// Directory transactions promote and roll back whole trees.
	{Key: "internal/detectionpack/import.go", Reason: "directory promotion with rollback of the replaced pack tree"},
	{Key: "internal/extpacks/meta_install.go", Reason: "directory promotion with backup and rollback"},
	{Key: "internal/extpacks/meta_transaction.go", Reason: "recoverable directory promotion with backup and rollback"},
	{Key: "internal/extpacks/package_transaction.go", Reason: "directory promotion of a staged package tree"},
	{Key: "internal/packagegeneration/materialize.go", Reason: "directory promotion of a verified package generation tree"},
	{Key: "internal/project/promotion_engine.go", Reason: "reservation-based promotion: renames are the transaction, not a write"},
	{Key: "internal/layeredfs/adapter/eagercopy_git.go", Reason: "builds a private Git directory in staging and promotes it whole"},
	{Key: "internal/layeredfs/recovery/salvage.go", Reason: "directory promotion of a completely salvaged quarantine tree"},

	// An external writer creates this staging file.
	{Key: "internal/db/store_schema.go", Reason: "SQLite VACUUM INTO writes the staged snapshot; the door uses its staging file"},

	// Developer tooling writes user-named export files outside any managed tree.
	{Key: "cmd/lycaon-debug/decide.go", Reason: "offline training exports to a path the developer names; nothing durable in the config tree"},

	// The door stages a whole replacement; it cannot express an append.
	{Key: "internal/blueprint/manager_impl.go", Reason: "append-only critic evidence log"},
	{Key: "internal/debugretention/writer.go", Reason: "bounded append stream truncates in place at a configured diagnostic cap"},
	{Key: "internal/inspector/jsonl_store.go", Reason: "append-only evidence journal"},

	// O_EXCL reports collisions without replacing existing bytes.
	{Key: "cmd/pw-vault-identity/store.go", Reason: "O_EXCL staging file published with link(2), so a stored identity is never replaced"},
	{Key: "internal/api/owner_only_posix.go", Reason: "O_EXCL create so the 0600 mode is in force before any byte lands"},
	{Key: "internal/api/owner_only_windows.go", Reason: "owner-only ACL applied to a freshly created manifest"},
	{Key: "internal/db/rebuild_copy.go", Reason: "O_EXCL staging database created with 0600 mode before SQLite populates and fseffect exchanges it"},
	{Key: "internal/filelock/filelock.go", Reason: "lock file opened for the flock it carries, not for its contents"},
	{Key: "internal/launchguard/stage_linux.go", Reason: "/dev/null opened read-write for the detached broker agent's standard streams"},
	{Key: "internal/packagegeneration/lifetime.go", Reason: "lock file opened for the flock it carries, not for its contents"},
	{Key: "internal/fileclone/clone_linux.go", Reason: "O_EXCL destination for a reflink clone; the ioctl replaces the write"},
	{Key: "internal/indexwatch/indexwatch.go", Reason: "O_EXCL destination while copying index file"},
	{Key: "internal/promptattach/docext/worker.go", Reason: "O_EXCL request file handed to a one-shot extraction worker"},
	{Key: "internal/scan/drivers/bundled/opengrep_io.go", Reason: "O_EXCL report path inside a scan run directory removed on cleanup"},
	{Key: "internal/sourcesnapshot/materialize_entry.go", Reason: "O_EXCL files in a private disposable scan tree; no replacement or durable publication"},

	// Probes write only inside a private temporary directory they remove before return.
	{Key: "internal/confine/denialprobe/denialprobe.go", Reason: "the attempt is the measurement: the kernel's answer is the result, and it removes what it creates"},
	{Key: "internal/layeredfs/adapter/viewconfine/viewconfine.go", Reason: "canary in a private proof directory removed with it"},
	{Key: "internal/layeredfs/adapter/fuselinux/fuselinux.go", Reason: "capability probe confined to a private temporary view"},
	{Key: "internal/layeredfs/adapter/lifecycle.go", Reason: "capability probe confined to a private temporary view"},

	// A worker's private repository is its own: nothing reads it as durable state until initialization
	// reports it ready, and a retained object's destination is its digest.
	{Key: "internal/layeredfs/privategit/executor_sync.go", Reason: "restores a file into the view's private worktree stage, which Core holds authoritatively"},
	{Key: "internal/layeredfs/privategit/init_capture.go", Reason: "fills a private Git staging directory its caller promotes whole"},
	{Key: "internal/layeredfs/privategit/init_repo.go", Reason: "promotes a fully captured private Git staging directory whole"},
	{Key: "internal/layeredfs/privategit/materialized_seed.go", Reason: "installs a seed's fully built private Git directory whole, before the view is published"},
	{Key: "internal/layeredfs/privategit/lower_provider.go", Reason: "opens a private Git metadata file for the view's own serving handle"},
	{Key: "internal/layeredfs/privategit/store.go", Reason: "first-open marker of a new object store; no prior content exists"},
	{Key: "internal/layeredfs/privategit/store_gc.go", Reason: "stream-then-name: a retained object's destination is its digest"},
	{Key: "internal/layeredfs/privategit/store_import.go", Reason: "stream-then-name: a retained object's destination is its digest"},
	{Key: "internal/layeredfs/checkpoint/git_publish.go", Reason: "fills a scratch bare repository, then names a verified pack by its digest"},
	{Key: "internal/layeredfs/adapter/nfsdarwin/nfsdarwin.go", Reason: "capability probe confined to a private temporary view"},

	// A view's engine performs its worker's own file operations: each is the worker's syscall, which a
	// replacement cannot express.
	{Key: "internal/layeredfs/backing_unix.go", Reason: "opens a view's backing file for the worker's own reads and writes"},
	{Key: "internal/layeredfs/handles_node.go", Reason: "creates the file a worker's open asked for"},
	{Key: "internal/layeredfs/rename_readdir.go", Reason: "performs the rename a worker asked for inside its private Git directory"},
	{Key: "internal/layeredfs/git_sync.go", Reason: "writes a worker's edit through to its private Git directory"},

	// A test names the pause marker in its own temporary directory and removes it to resume.
	{Key: "internal/layeredfs/adapter/flushhelper/flushhelper.go", Reason: "a test's pause marker holding the helper's PID"},

	// Worker branches are unpublished until snapshot completion names them complete.
	{Key: "internal/layeredfs/adapter/eagercopy_git_helpers.go", Reason: "fills a private Git staging directory its caller promotes whole"},
	{Key: "internal/layeredfs/adapter/eagercopy_content.go", Reason: "O_EXCL file in an unpublished worker tree; SnapshotComplete publishes the assembled tree"},
	{Key: "internal/layeredfs/adapter/eagercopy_lower.go", Reason: "applies a parent layer to an unpublished worker tree; SnapshotComplete publishes the assembled tree"},
	{Key: "internal/layeredfs/recovery/reconstruct.go", Reason: "reconstructs an unpublished branch under its provision lock; SnapshotComplete publishes it"},
	{Key: "internal/layeredfs/recovery/stacked.go", Reason: "reconstructs an unpublished branch under its provision lock; SnapshotComplete publishes it"},

	// Copies into a staging tree the caller promotes or discards whole.
	{Key: "internal/backup/stage.go", Reason: "streams into an exclusive staging tree the caller promotes"},
	{Key: "internal/extpacks/install.go", Reason: "copies a source tree into staging before package_transaction promotes it"},
	{Key: "internal/workspacebaseline/materialize.go", Reason: "rebuilds an unpublished branch under its provision lock; SnapshotComplete publishes the assembled tree"},

	// Desktop trash relocations and spec metadata outside workspace transactions.
	{Key: "internal/desktoptrash/trash_linux.go", Reason: "implements FreeDesktop Trash specification by writing .trashinfo and relocating to files directory"},

	// These paths have no prior content to preserve.
	{Key: "internal/browserengine/provision.go", Reason: "copies a downloaded browser tree into a fresh provisioning directory, then publishes a completion marker"},
	{Key: "internal/decide/bialy/provision.go", Reason: "renames each digest-verified checkpoint file into its model directory, then publishes a completion marker"},
	{Key: "internal/hostlock/hostlock.go", Reason: "advisory pid stamp written under the lock it names; best-effort by design"},
	{Key: "internal/preflight/probes.go", Reason: "writability probe: written to prove the mount accepts a write, then removed"},
	{Key: "cmd/lycaon-perf/fixtures.go", Reason: "generated disposable benchmark tree under a fresh scratch root; no durable or prior content exists"},
	{Key: "internal/testdbfixture/open.go", Reason: "clones cached database template into disposable test fixture path"},

	// Maintenance commands write generated checkout artifacts.
	{Key: "internal/approvalregistry/registry.go", Reason: "generated registry artifact written by ./task codegen:approval-explanations"},
	{Key: "internal/oar/corpus.go", Reason: "seeds the conformance fixture corpus into a caller-named output directory"},
	{Key: "internal/oar/integrity.go", Reason: "generated guidance registry written by ./task codegen:guidance-registry"},

	// CLI output failures are returned to the operator.
	{Key: "cmd/lycaon-debug/eval_tool_usage.go", Reason: "operator-selected report path"},
	{Key: "cmd/lycaon/completion.go", Reason: "shell completion script written to an operator-selected path"},
	{Key: "cmd/lycaon/extensions_theme.go", Reason: "theme export written to an operator-selected path"},
	{Key: "cmd/oar-import-invariant/main.go", Reason: "invariant import written to an operator-selected path"},
	{Key: "cmd/decide-rerank/eval.go", Reason: "evaluation report written to an operator-selected path"},
	{Key: "cmd/decide-rerank/units.go", Reason: "training units written to an operator-selected path"},
}

// runVarsHome is the vars primitive's own file.
const runVarsHome = "internal/workflow/runstate/variables.go"

// runVarsCallerHoldsLock participates in a wider caller-managed critical section.
const runVarsCallerHoldsLock = "stampLocked"

var runVars = []laws.Rule{{
	// Scaffold vars are written only by the primitive, which loads the run
	// under its lock immediately before the compare-and-set.
	Name:    "run-vars-written-by-the-primitive",
	Anchors: []laws.Anchor{laws.GoPath(runVarsHome), laws.Func("internal/workflow/runstate", "Variables."+runVarsCallerHoldsLock)},
	Exempt: []laws.Exemption{{
		Key:    "internal/workflow/approval_service.go",
		Reason: "CommitBlueprintApproval commits vars and the approval record together; its writers keep the fresh-load shape",
	}},
	Check: func(r *laws.Run) {
		home := false
		for _, f := range r.GoFiles(laws.Production, "internal") {
			writers := runVarsWritingFuncs(f.AST)
			if len(writers) > 0 && r.Exempt(f.Rel) {
				for _, offence := range runVarsFreshLoadOffences(f.Rel, writers) {
					r.Report("%s", offence)
				}
				continue
			}
			if f.Rel == runVarsHome {
				home = len(writers) > 0
				for _, offence := range runVarsFreshLoadOffences(f.Rel, writers) {
					r.Report("%s", offence)
				}
				continue
			}
			for _, w := range writers {
				r.ReportAt(w.decl.Pos(), "%s writes vars outside %s", w.name, runVarsHome)
			}
		}
		if !home {
			r.Report("%s commits no vars", runVarsHome)
		}
	},
}, {
	// A writer that accepts a run could commit against a stale revision; it
	// takes a RunVarsMutation instead.
	Name:    "run-vars-primitive-takes-no-run",
	Anchors: []laws.Anchor{laws.GoPath(runVarsHome)},
	Check: func(r *laws.Run) {
		f := r.GoFile(runVarsHome)
		f.Funcs(func(fn *ast.FuncDecl) {
			for _, param := range fn.Type.Params.List {
				if star, ok := param.Type.(*ast.StarExpr); ok && laws.TypeName(star.X) == "WorkflowRun" {
					r.ReportAt(param.Pos(), "%s accepts a caller-supplied run", fn.Name.Name)
				}
			}
		})
	},
}}

type runVarsWriter struct {
	name      string
	decl      *ast.FuncDecl
	writePos  token.Pos
	loadPos   token.Pos
	lockPos   token.Pos
	hasLoad   bool
	hasLock   bool
	readsVars bool
}

// runVarsWritingFuncs returns each function that commits the vars row, with the
// positions of its lock, its run load, and its write.
func runVarsWritingFuncs(file *ast.File) []runVarsWriter {
	var out []runVarsWriter
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		w := runVarsWriter{name: fn.Name.Name, decl: fn}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "UpdateVars", "CommitBlueprintApproval":
				if !w.writePos.IsValid() {
					w.writePos = sel.Pos()
				}
			case "loadRun", "ActiveBySession", "Get":
				if !w.hasLoad {
					w.hasLoad, w.loadPos = true, sel.Pos()
				}
			case "Lock":
				if !w.hasLock {
					w.hasLock, w.lockPos = true, sel.Pos()
				}
			case "GetScaffoldVars":
				w.readsVars = true
			}
			return true
		})
		if w.writePos.IsValid() {
			out = append(out, w)
		}
	}
	return out
}

// runVarsFreshLoadOffences reports a writer whose CAS could carry a revision
// read before its critical section began.
func runVarsFreshLoadOffences(rel string, writers []runVarsWriter) []string {
	var out []string
	for _, w := range writers {
		if !w.hasLoad {
			out = append(out, fmt.Sprintf("%s: %s commits vars without loading the run in the same function",
				rel, w.name))
			continue
		}
		if w.loadPos > w.writePos {
			out = append(out, fmt.Sprintf("%s: %s loads the run after committing vars", rel, w.name))
			continue
		}
		if w.name == runVarsCallerHoldsLock {
			// The caller holds the lock; this function loads the run.
			continue
		}
		if !w.hasLock {
			out = append(out, fmt.Sprintf("%s: %s commits vars without lockRunVars", rel, w.name))
			continue
		}
		if w.lockPos > w.loadPos {
			out = append(out, fmt.Sprintf("%s: %s loads the run before taking lockRunVars — "+
				"the compare-and-set would carry a pre-lock revision", rel, w.name))
		}
	}
	return out
}
