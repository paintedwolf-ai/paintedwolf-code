package settings_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/sensitivepath"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Batch decisions disclose their highest-risk path crossing.
func TestDeclaredFileTargetDisclosesBatchWorstCase(t *testing.T) {
	t.Parallel()

	projectDir := t.TempDir()
	storeDir := filepath.Join(projectDir, "vault")
	testutil.FailErr(t, "mkdir vault", os.MkdirAll(storeDir, 0o755))
	protectedFile := filepath.Join(storeDir, "credentials.json")

	catalogDir := t.TempDir()
	yamlBody := "version: 1\nlocations:\n  - id: test-credential-store\n    title: Test credential store\n    mode: any\n    protected: true\n    paths: [\"" + filepath.ToSlash(storeDir) + "\"]\n"
	testutil.FailErr(t, "write catalog", os.WriteFile(filepath.Join(catalogDir, "test.yaml"), []byte(yamlBody), 0o644))
	cat, err := sensitivepath.Load(sensitivepath.Dir(catalogDir))
	testutil.FailErr(t, "load catalog", err)

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	sources := settings.NoSources()
	sources.Locations = cat
	g := settings.NewRuleApprovalGate(store, sources)

	// The ordinary path precedes the protected target.
	outsideRootsFile := filepath.Join(t.TempDir(), "unrelated", "notes.txt")

	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "copy",
Files: []string{outsideRootsFile, protectedFile},
},
Scope: hitl.ActionScope{
ProjectDir: projectDir,
SessionID: "sess-batch-worst-case",
},
}
	res, err := g.Evaluate(context.Background(), action)
	testutil.FailErr(t, "Evaluate", err)
	if res == nil || res.Decision == nil {
		t.Fatalf("expected an ask decision, got %+v", res)
	}
	if got := citedValue(res.Decision.Cited, "file.path"); got != protectedFile {
		t.Fatalf("card discloses %q, want the protected target %q", got, protectedFile)
	}
	if got := citedValue(res.Decision.Cited, "file.other_count"); got != "1" {
		t.Fatalf("file.other_count = %q, want 1 (the ordinary outside-roots crossing)", got)
	}
}

func citedValue(cited []gate.Fact, key string) string {
	for _, f := range cited {
		if f.Key == key {
			return f.Value
		}
	}
	return ""
}
