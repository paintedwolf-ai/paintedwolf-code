package hostcontracts

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/harnessfixture"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestHarnessPreparesOverlaysThroughAuthenticatedApplicationRoute(t *testing.T) {
	t.Setenv(configdir.EnvDev, "1")
	t.Setenv(configdir.EnvHarness, "1")
	database := testdbfixture.Open(t, "harness.db")
	projects := project.NewSQLRegistry(database)
	root := t.TempDir()
	testutil.FailErr(t, "write baseline", os.WriteFile(filepath.Join(root, "app.py"), []byte("value = 0\n"), 0o600))
	attached, err := projects.Create(t.Context(), project.CreateParams{Roots: []project.AttachRootParams{{Path: root}}})
	testutil.FailErr(t, "attach fixture", err)
	sessions := store.NewSQL(database)
	parent, err := sessions.Create(t.Context(), wire.CreateSessionRequest{WorkspaceRootID: attached.Roots[0].ID}, attached.ID)
	testutil.FailErr(t, "create session", err)
	queue := worker.NewSQLQueue(database, 2)
	ledger := sourceledger.New(database, filepath.Join(testbaseline.DataDir(t, database), "source-content"))
	queue.SetBaselineStore(ledger.BaselineStore())
	queue.SetProjectStore(projects)
	queue.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(testbaseline.DataDir(t, database), "worker-branches"), t.TempDir()))
	server := hostapi.NewServer(contractfixture.RequiredTestDeps(t, hostapi.Dependencies{Core: hostapi.CoreDependencies{Store: sessions, Projects: projects}, Workflow: hostapi.WorkflowDependencies{Workers: queue}}), nil, hostapi.TestAPIToken)
	payload, err := json.Marshal(harnessfixture.Request{SessionID: parent.ID, Setup: harnessfixture.Setup{Overlays: []harnessfixture.Overlay{
		{Label: "one", Files: map[string]string{"app.py": "value = 1\n"}},
		{Label: "two", Files: map[string]string{"app.py": "value = 2\n"}},
	}}})
	testutil.FailErr(t, "encode fixture setup", err)
	request := httptest.NewRequest(http.MethodPost, "/harness/overlays", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+hostapi.TestAPIToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("fixture endpoint: %d %s", response.Code, response.Body.String())
	}
	var evidence harnessfixture.Evidence
	testutil.FailErr(t, "decode prepared overlays", json.Unmarshal(response.Body.Bytes(), &evidence))
	if len(evidence.Overlays) != 2 {
		t.Fatalf("prepared overlays: %+v", evidence)
	}
	for _, overlay := range evidence.Overlays {
		task, ok := queue.Get(overlay.JobID)
		if !ok || task.ParentSessionID != parent.ID || task.MergeStatus != wire.WorkerMergeStatusPending {
			t.Fatalf("application overlay: %+v", task)
		}
	}
}
