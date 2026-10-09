package workflow_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestComposeUpsertPersistsEffectiveSummary(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)

	sqlDB := testdbfixture.Open(t, "summary.db")

	sessionStore := store.NewSQL(sqlDB)
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := sessionStore.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessionStore.Create failed", err)
	wfSessionStore := workflowdrafts.NewSQL(sqlDB)
	policy, err := workflowcomposition.LoadComposePolicy()
	testutil.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	composer := &workflowcomposition.Composer{
		SessionStore: wfSessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
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
	result, err := composer.Compose(context.Background(), workflowcomposition.ComposeRequest{
		SessionID: sess.ID, ManifestYAML: []byte(manifest), SessionPosture: sess.Posture, CreatedBy: "coordinator",
	})
	testutil.FailErr(t, "compose workflow manifest", err)
	rec, err := wfSessionStore.Get(context.Background(), sess.ID, "hotfix-session", "1.0.0")
	testutil.FailErr(t, "wfSessionStore.Get failed", err)
	if rec.EffectiveSummary.CoordinatorBrief != result.EffectiveSummary.CoordinatorBrief {
		t.Fatalf("stored brief = %q want %q", rec.EffectiveSummary.CoordinatorBrief, result.EffectiveSummary.CoordinatorBrief)
	}
	if len(rec.EffectiveSummary.Phases) != len(result.EffectiveSummary.Phases) {
		t.Fatalf("stored phases = %d want %d", len(rec.EffectiveSummary.Phases), len(result.EffectiveSummary.Phases))
	}
	if err := wfSessionStore.Delete(context.Background(), sess.ID, "hotfix-session", "1.0.0"); err != nil {
		testutil.FailErr(t, "wfSessionStore.Delete failed", err)
	}
	if _, err := wfSessionStore.Get(context.Background(), sess.ID, "hotfix-session", "1.0.0"); err == nil {
		t.Fatal("expected row deleted")
	}
}
