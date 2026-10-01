package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNativeScratchAccessDoesNotAskForAnOutsideFolderGrant(t *testing.T) {
	stageBundledApprovals(t, nil)
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "global.yaml"))
	testutil.FailErr(t, "create approval store", err)
	g := settings.NewRuleApprovalGate(store, settings.NoSources())
	project, scratch := t.TempDir(), t.TempDir()
	for _, tool := range []string{"read", "write"} {
		result, err := g.Evaluate(t.Context(), hitl.ProposedAction{
			Tool: tool, ProjectDir: project, Files: []string{filepath.Join(scratch, "notes.txt")},
		})
		testutil.FailErr(t, "evaluate scratch "+tool, err)
		if result.Required() {
			t.Fatalf("%s asked for scratch authority: %+v", tool, result)
		}
	}
}
