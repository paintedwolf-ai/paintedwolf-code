//go:build integration

package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestComposePolicyBlocksVetWithoutSecurityGate(t *testing.T) {
	c := testComposer(t)
	manifest := `id: vet-skip-security
version: 1.0.0
extends: plan@1.0.0
initial_posture: vet
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	_, err := c.Compose(context.Background(), ComposeRequest{
		SessionID:      "sess-vet",
		ManifestYAML:   []byte(manifest),
		SessionPosture: wire.SessionPostureVet,
		CreatedBy:      "coordinator",
	})
	var vf *ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	found := false
	for _, e := range vf.Errors {
		if e.Code == "required_gate_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %+v", vf.Errors)
	}
}

func TestComposePolicyBlocksOrchestrateWithoutCloseout(t *testing.T) {
	c := testComposer(t)
	manifest := `id: orch-no-closeout
version: 1.0.0
extends: plan@1.0.0
initial_posture: orchestrate
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    complete_when: plan_stub_valid
`
	_, err := c.Compose(context.Background(), ComposeRequest{
		SessionID:      "sess-orch",
		ManifestYAML:   []byte(manifest),
		SessionPosture: wire.SessionPostureOrchestrate,
		CreatedBy:      "coordinator",
	})
	var vf *ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	found := false
	for _, e := range vf.Errors {
		if e.Code == "required_gate_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %+v", vf.Errors)
	}
}

func TestComposeTemplateThenStartRun(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	policy, err := LoadComposePolicy()
	testutil.FailErr(t, "LoadComposePolicy failed", err)
	templates, err := LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "LoadTemplatesFromDir failed", err)

	sqlDB := testdbfixture.Open(t, "template.db")

	sessionStore := store.NewSQL(sqlDB)
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := sessionStore.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "sessionStore.Create failed", err)
	wfSessionStore := NewSessionWorkflowSQLStore(sqlDB)
	composer := &Composer{
		SessionStore: wfSessionStore,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
		Templates:    templates,
	}
	result, err := composer.ComposeFromTemplate(context.Background(), ComposeFromTemplateRequest{
		SessionID:      sess.ID,
		TemplateID:     "hotfix-template",
		Params:         map[string]any{"workflow_id": "my-hotfix"},
		SessionPosture: sess.Posture,
		CreatedBy:      "coordinator",
	})
	testutil.FailErr(t, "composer.ComposeFromTemplate failed", err)
	if result.Summary.ID != "my-hotfix" {
		t.Fatalf("summary id = %q", result.Summary.ID)
	}

	manifestRegistry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	mgr := NewManager(NewSQLStore(sqlDB), sessionStore, manifestRegistry, nil)
	mgr.Resolver = ManifestResolver{SessionStore: wfSessionStore}
	WireBlueprintDepsForTest(mgr, dir)
	run, err := startRun(context.Background(), mgr, sess.ID, "my-hotfix", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q", run.CurrentPhase)
	}
}
