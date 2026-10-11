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
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestComposeThenStartRun(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)

	sqlDB := testdbfixture.Open(t, "wf.db")

	sessionStore := store.NewSQL(sqlDB)
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := sessionStore.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessionStore.Create failed", err)
	wfSessionStore := workflowdrafts.NewSQL(sqlDB)
	composer := &workflowcomposition.Composer{
		SessionStore: wfSessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       mustLoadComposePolicy(t),
	}
	manifest := `id: hotfix-session
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
		SessionID: sess.ID, ManifestYAML: []byte(manifest), SessionPosture: sess.Posture, CreatedBy: "coordinator",
	}); err != nil {
		t.Fatal(err)
	}
	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	runStore := workflowpersistence.New(sqlDB)
	mgr := NewManager(runStore, sessionStore, manifestRegistry, nil)
	mgr.Resolver.SessionStore = wfSessionStore
	WireBlueprintDepsForTest(mgr, dir)
	run, err := startRun(context.Background(), mgr, sess.ID, "hotfix-session", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q", run.CurrentPhase)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), run.ID)
	testutil.FailErr(t, "mgr.Store.Runs.GetScaffoldVars failed", err)
	if vars["workflow_compose_summary_id"] != workflowdef.ManifestKey("hotfix-session", "1.0.0") {
		t.Fatalf("vars = %v", vars)
	}
}

func mustLoadComposePolicy(t *testing.T) *workflowcomposition.ComposePolicy {
	t.Helper()
	p, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	return p
}
