package store

import (
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLSpillReferencesFollowDurableTranscriptAndCompactionViews(t *testing.T) {
	dataDir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dataDir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQL(sqlDB)
	store.SetDataDir(dataDir)
	sess, err := store.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	hostDir, err := project.EnsureHostDataDir(dataDir, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "ensure project data", err)

	writeSpill := func(body string) (string, string) {
		t.Helper()
		out := tooloutput.SpillWholeToolOutput(hostDir, tooloutput.Screened(body), 0)
		if out.SpillPath == "" {
			t.Fatal("spill path was not written")
		}
		return out.SpillPath, tooloutput.DiskPath(hostDir, out.SpillPath)
	}
	assertExists := func(path, step string) {
		t.Helper()
		_, err := os.Lstat(path)
		testutil.FailErr(t, step, err)
	}
	assertMissing := func(path, step string) {
		t.Helper()
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s: path still exists: %v", step, err)
		}
	}

	compactionRel, compactionPath := writeSpill(strings.Repeat("compaction body ", 32))
	testutil.FailErr(t, "put compaction view", store.PutCompactionView(t.Context(), sess.ID, CompactionView{
		Generation: 1,
		Messages: []api.Message{{
			Role: api.MessageRoleTool, Content: "full output: " + compactionRel,
			ToolResult: &api.ToolResult{Content: compactionRel},
		}},
	}))
	assertExists(compactionPath, "compaction spill retained")
	settled := time.Now().Add(-2 * time.Minute)
	testutil.FailErr(t, "age compaction spill", os.Chtimes(compactionPath, settled, settled))
	testutil.FailErr(t, "replace compaction view", store.PutCompactionView(t.Context(), sess.ID, CompactionView{
		Generation: 2,
		Messages:   []api.Message{{Role: api.MessageRoleAssistant, Content: "summary"}},
	}))
	assertMissing(compactionPath, "replaced compaction spill reclaimed")

	spoofRel, spoofPath := writeSpill(strings.Repeat("spoofed user path ", 32))
	testutil.FailErr(t, "append user text", store.AppendMessages(t.Context(), sess.ID, api.Message{
		Role: api.MessageRoleUser, Content: "please inspect " + spoofRel,
	}))
	testutil.FailErr(t, "age unreferenced spill", os.Chtimes(spoofPath, settled, settled))

	toolRel, toolPath := writeSpill(strings.Repeat("durable tool body ", 32))
	testutil.FailErr(t, "append tool result", store.AppendMessages(t.Context(), sess.ID, api.Message{
		Role: api.MessageRoleTool, Content: "full output: " + toolRel,
		ToolResult: &api.ToolResult{Content: toolRel, Outcome: api.ToolResultOutcomeCompleted},
	}))
	store = NewSQL(sqlDB)
	store.SetDataDir(dataDir)
	_, freshPath := writeSpill(strings.Repeat("current process body ", 32))
	_, err = store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "activate project spill store", err)
	assertMissing(spoofPath, "unreferenced spill reclaimed")
	assertExists(toolPath, "tool spill retained")
	assertExists(freshPath, "current process spill retained")
	testutil.FailErr(t, "age tool spill", os.Chtimes(toolPath, settled, settled))
	testutil.FailErr(t, "delete session", store.Delete(t.Context(), sess.ID))
	assertMissing(toolPath, "deleted session spill reclaimed")
}

func TestSQLSpillReconciliationAdvancesInBoundedBatches(t *testing.T) {
	dataDir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(dataDir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	store := NewSQL(sqlDB)
	store.SetDataDir(dataDir)
	sess, err := store.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	hostDir, err := project.EnsureHostDataDir(dataDir, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "ensure project data", err)
	settled := time.Now().Add(-2 * time.Minute)
	for i := range spillReconcileBatchSize + 1 {
		body := strings.Repeat("orphan "+string(rune('a'+i%26)), i+2)
		out := tooloutput.SpillWholeToolOutput(hostDir, tooloutput.Screened(body), 0)
		if out.SpillPath == "" {
			t.Fatal("spill path was not written")
		}
		path := tooloutput.DiskPath(hostDir, out.SpillPath)
		testutil.FailErr(t, "age orphan spill", os.Chtimes(path, settled, settled))
	}
	store = NewSQL(sqlDB)
	store.SetDataDir(dataDir)
	_, err = store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "advance first spill batch", err)
	spillDir := filepath.Join(hostDir, tooloutput.ToolOutputSpillDir)
	entries, err := os.ReadDir(spillDir)
	testutil.FailErr(t, "list remaining spills", err)
	if len(entries) != 1 {
		t.Fatalf("remaining spills after first batch = %d want 1", len(entries))
	}
	_, err = store.Get(t.Context(), sess.ID)
	testutil.FailErr(t, "advance second spill batch", err)
	entries, err = os.ReadDir(spillDir)
	testutil.FailErr(t, "list reconciled spills", err)
	if len(entries) != 0 {
		t.Fatalf("remaining spills after second batch = %d want 0", len(entries))
	}
}
