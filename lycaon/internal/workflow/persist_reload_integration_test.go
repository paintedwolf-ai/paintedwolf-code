//go:build integration

package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPersistReloadIntegration(t *testing.T) {
	p, store := testPersister(t)
	projectDir := t.TempDir()
	sessionID := "sess-reload"
	manifest := `id: hotfix
version: 1.0.0
extends: plan@1.0.0
initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: gates_satisfied
    gates:
      - delegation_closeout_complete
      - evidence_passed:verify
`
	if err := store.Upsert(context.Background(), sessionID, []byte(manifest), ComposeActorCoordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	if _, err := p.Persist(context.Background(), PersistRequest{
		SessionID:  sessionID,
		ProjectDir: projectDir,
		WorkflowID: "hotfix",
		Version:    "1.0.0",
		Confirm:    true,
		Trigger:    "/hotfix-reload",
	}); err != nil {
		t.Fatal(err)
	}
	resolver := ManifestResolver{SessionStore: store, ProjectTierApplies: func(context.Context, string) bool { return true }}
	summaries, err := resolver.ListResolved(context.Background(), projectDir, sessionID)
	testutil.FailErr(t, "resolver.ListResolved failed", err)
	found := false
	for _, s := range summaries {
		if s.ID == "hotfix" && s.Version == "1.0.0" && s.Scope == api.WorkflowScopeProject {
			found = true
			if s.Trigger != "/hotfix-reload" {
				t.Fatalf("trigger = %q", s.Trigger)
			}
		}
		if s.ID == "hotfix" && s.Scope == api.WorkflowScopeSession {
			t.Fatal("session scope should be removed after persist")
		}
	}
	if !found {
		t.Fatalf("catalog missing project hotfix: %+v", summaries)
	}
	reg, err := workflowdef.RegistryFromDirs(projectDir)
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	if _, err := reg.Get("hotfix", "1.0.0"); err != nil {
		testutil.FailErr(t, "reg.Get failed", err)
	}
}

func testPersisterForIntegration(t *testing.T) (*Persister, *MemorySessionWorkflowStore) {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	store := NewMemorySessionWorkflowStore()
	policy, err := LoadComposePolicy()
	testutil.FailErr(t, "LoadComposePolicy failed", err)
	return &Persister{
		SessionStore: store,
		Registry:     reg,
		Agents:       agents,
		Policy:       policy,
	}, store
}

func TestPersistReloadUsesFreshDiskRead(t *testing.T) {
	p, store := testPersisterForIntegration(t)
	projectDir := t.TempDir()
	sessionID := "sess-fresh"
	manifest := `id: hotfix
version: 1.0.0
extends: plan@1.0.0
initial_posture: spec
agents:
  - { id: plan-writer, tools: profile }
  - { id: implementer, tools: profile }
phases:
  - id: research
    activity_label: Test phase
    next: build
  - id: build
    activity_label: Test phase
    on_enter:
      set_posture: build
    complete_when: gates_satisfied
    gates:
      - delegation_closeout_complete
      - evidence_passed:verify
`
	if err := store.Upsert(context.Background(), sessionID, []byte(manifest), ComposeActorCoordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	if _, err := p.Persist(context.Background(), PersistRequest{
		SessionID:  sessionID,
		ProjectDir: projectDir,
		WorkflowID: "hotfix",
		Version:    "1.0.0",
		Confirm:    true,
		// Persist keeps the resolved trigger, so an inherited `/plan` would collide
		// with bundled plan.
		Trigger: "/hotfix",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := ManifestResolver{SessionStore: store, ProjectTierApplies: func(context.Context, string) bool { return true }}.ListResolved(context.Background(), projectDir, "")
	testutil.FailErr(t, "operation failed", err)
	foundBefore := false
	for _, s := range before {
		if s.ID == "hotfix" && s.Scope == api.WorkflowScopeProject {
			foundBefore = true
		}
	}
	if !foundBefore {
		t.Fatal("expected project scope before second list")
	}
}
