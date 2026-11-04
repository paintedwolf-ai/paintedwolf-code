package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRecordFindingAppendUsesDelegationScopeNotOverlayToolDir(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "native", "reporting", "record_finding.go")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read record_finding.go", err)
	text := string(raw)
	if !strings.Contains(text, "scopeKey(ctx, tctx.Identity.SessionID)") {
		t.Fatal("record_finding must resolve delegation scope via FindingsScopeKey(sessionID)")
	}
	if strings.Contains(text, "return strings.TrimSpace(tctx.ProjectDir)") {
		t.Fatal("record_finding must not fall back to tctx.ProjectDir for findings scope")
	}
	if !strings.Contains(text, "store.Append(ctx, rootSession,") {
		t.Fatal("record_finding must append under the resolved root session, not an overlay directory")
	}
	if strings.Contains(text, "store.Append(tctx.ProjectDir") {
		t.Fatal("record_finding must not append findings keyed on tctx.ProjectDir overlay")
	}
}

func TestHandoffInitKeysOffSessionProjectDirNotOverlay(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "call", "register_reservations.go")
	raw, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read register_reservations.go", err)
	text := string(raw)
	if strings.Contains(text, "return strings.TrimSpace(tctx.ProjectDir)") {
		t.Fatal("handoff_init must not fall back to tctx.ProjectDir for delegation scope")
	}
}
