package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// mintSeamFiles are the only files that construct a tool_approval checkpoint
// request, so one place interrupts a person and one place decides why. The
// sandbox capability brokers share awaitSandboxAsk, so only it appears here;
// another broker entry would be a second copy of that lifecycle.
var mintSeamFiles = map[string]string{
	"lycaon/internal/toolexecution/executor_tool_approval.go": "the executor's raise-and-wait seam",
	"lycaon/internal/session/sandbox_ask_broker.go":           "the sandbox capability ask lifecycle",
	"lycaon/internal/toolexecution/socket_approval.go":        "the local-socket capability card",
	"lycaon/internal/toolexecution/direct_ip_capability.go":   "the direct-network capability card",
}

// TestApprovalsHaveOneMintSeam keeps approval creation on reviewed seams.
func TestApprovalsHaveOneMintSeam(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offenders []string
	walkGoSources(t, filepath.Join(root, "lycaon"), func(rel, src string) {
		if _, allowed := mintSeamFiles[rel]; allowed || strings.HasSuffix(rel, "_test.go") {
			return
		}
		if strings.Contains(src, "hitl.CheckpointRequest{") &&
			strings.Contains(src, "api.CheckpointKindToolApproval") {
			offenders = append(offenders, rel)
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("tool_approval minted outside the seam: %v\nroute these through the executor's raise path so one gate decides every interruption", offenders)
	}
}

func walkGoSources(t *testing.T, root string, visit func(rel, src string)) {
	t.Helper()
	repo := contractcheck.RepoRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(repo, path)
		if relErr != nil {
			return nil
		}
		visit(filepath.ToSlash(rel), string(raw))
		return nil
	})
	contractcheck.FailErr(t, "walk go files", err)
}
