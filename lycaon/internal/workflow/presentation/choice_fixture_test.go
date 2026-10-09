package presentation_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
)

func choiceTransitionsTestMgr(t *testing.T) (*workflow.RunManager, *store.SQL, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "choice-tr.db")

	path := filepath.Join("..", "..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	m = workflowdef.FinalizeManifest(m)

	sessStore := store.NewSQL(sqlDB)
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{workflowdef.ManifestKey(m.ID, m.Version): m})
	wfStore := workflowpersistence.New(sqlDB)
	mgr := workflow.NewManager(wfStore, sessStore, reg, nil)
	dir := t.TempDir()
	workflow.WireBlueprintDepsForTest(mgr, dir)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	return mgr, sessStore, dir
}
