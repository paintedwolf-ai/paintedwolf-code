package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Branch mutations and process tools require branch attachment.
func TestWorkerBranchCatalogCoversWriteAndExecDoors(t *testing.T) {
	t.Parallel()
	catalog := map[string]struct{}{}
	for _, name := range workerBranchCatalog(t) {
		catalog[name] = struct{}{}
	}
	required := map[string]struct{}{
		"write": {}, "edit": {}, "replace_lines": {}, "restore_version": {}, "code_rewrite": {},
		"delete": {}, "mkdir": {}, "copy": {}, "move": {}, "chmod": {}, "chown": {},
		"extract_archive": {}, "git_commit": {}, "git_restore": {},
		"command": {}, "verify": {}, "terminal_open": {},
	}
	for name := range required {
		if _, ok := catalog[name]; !ok {
			t.Errorf("requires_worker_branch missing %q", name)
		}
		if !tools.RequiresWorkerBranch(name) {
			t.Errorf("codegen RequiresWorkerBranch(%q)=false", name)
		}
	}
	for name := range catalog {
		if strings.HasPrefix(name, "list_") || name == "read" || name == "grep" || name == "find" {
			t.Errorf("survey tool %q must not require worker branch", name)
		}
	}
}

// A tool with no root to isolate has no branch to claim, and a read-scoped worker
// that calls one would be rejected for isolation it never needed.
func TestWorkerBranchCatalogExcludesRootIndependentTools(t *testing.T) {
	t.Parallel()
	for _, name := range workerBranchCatalog(t) {
		capability, declared := toolcontract.MultiRootOf(name)
		if !declared {
			t.Errorf("worker-branch tool %q has no declared multi-root capability — "+
				"add it to multi_root in native-tools.yaml", name)
			continue
		}
		if capability == toolcontract.MultiRootNone {
			t.Errorf("root-independent tool %q must not require a worker branch", name)
		}
	}
}

// [OAR-EVAL-1] Every intrinsic branch refusal reaches the same anchor, including newly registered tools.
func TestWorkerBranchClaimPolicyConsumesActualRefusals(t *testing.T) {
	contractcheck.FailErr(t, "install anchors", anchorcatalog.InstallBundled())
	loader, err := oar.NewLoader(filepath.Join(contractcheck.RepoRoot(t), "schemas"))
	contractcheck.FailErr(t, "create policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	contractcheck.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	for _, tool := range append(workerBranchCatalog(t), "future_branch_tool") {
		for _, code := range []string{"WORKER_BRANCH_CLAIM_FAILED", "UNRELATED_REFUSAL"} {
			gc := oar.NewGuardContext()
			gc.Session.SessionID = t.Name() + tool + code
			gc.Invocation.Tool = tool
			gc.ObservedRejectCode = code
			result, err := pipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
			contractcheck.FailErr(t, "evaluate branch refusal", err)
			if code == "WORKER_BRANCH_CLAIM_FAILED" {
				if result == nil || result.Decision == nil || result.Decision.Code != code || len(result.Decision.Copy) != 5 {
					t.Fatalf("%s refusal lost policy: %+v", tool, result)
				}
			} else if result != nil && result.Decision != nil && result.Decision.Code == "WORKER_BRANCH_CLAIM_FAILED" {
				t.Fatalf("%s unrelated refusal selected branch policy", tool)
			}
		}
	}
}

func workerBranchCatalog(t *testing.T) []string {
	t.Helper()
	cfg, err := nativemanifest.Load()
	if err != nil {
		t.Fatalf("nativemanifest.Load: %v", err)
	}
	return cfg.WorkerBranchTools()
}
