//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStartSessionTierWorkflow(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "wf.db")

	sessStore := store.NewSQL(sqlDB)
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := sessStore.Create(context.Background(), api.CreateSessionRequest{
		Posture: api.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessStore.Create failed", err)

	sessionWFStore := workflowdrafts.NewSQL(sqlDB)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	composer := &workflowcomposition.Composer{
		SessionStore: sessionWFStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       mustLoadComposePolicy(t),
	}
	manifestYAML := `id: hotfix-session
version: 1.0.0
extends: plan@1.0.0
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: delegation_closeout_complete
`
	if _, err := composer.Compose(context.Background(), workflowcomposition.ComposeRequest{
		SessionID: sess.ID, ManifestYAML: []byte(manifestYAML), SessionPosture: sess.Posture, CreatedBy: "coordinator",
	}); err != nil {
		t.Fatal(err)
	}

	bundledReg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	runStore := workflowpersistence.New(sqlDB)
	mgr := NewManager(runStore, sessStore, bundledReg, nil)
	mgr.Resolver.SessionStore = sessionWFStore
	WireBlueprintDepsForTest(mgr, dir)

	run, err := startRun(context.Background(), mgr, sess.ID, "hotfix-session", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if run.WorkflowID != "hotfix-session" {
		t.Fatalf("workflow_id = %q", run.WorkflowID)
	}
	if run.CurrentPhase != "research" {
		t.Fatalf("current_phase = %q", run.CurrentPhase)
	}
}
