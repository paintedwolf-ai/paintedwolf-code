package composition_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPersister(t *testing.T) (*workflowcomposition.Persister, *workflowdrafts.Memory) {
	t.Helper()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "build conditions registry", err)
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	store := workflowdrafts.NewMemory()
	return &workflowcomposition.Persister{
		SessionStore: store,
		Registry:     reg,
		Agents:       agents,
		Policy:       testComposePolicy(t),
	}, store
}

func TestPersistRejectsWithoutConfirm(t *testing.T) {
	p, store := testPersister(t)
	manifest := `id: hotfix
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
	if err := store.Upsert(context.Background(), "sess-1", []byte(manifest), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	_, err := p.Persist(context.Background(), workflowcomposition.PersistRequest{
		SessionID:  "sess-1",
		ProjectDir: t.TempDir(),
		WorkflowID: "hotfix",
		Version:    "1.0.0",
		Confirm:    false,
	})
	var notConfirmed *workflowcomposition.PersistNotConfirmedError
	if !errors.As(err, &notConfirmed) {
		t.Fatalf("err = %v", err)
	}
}

func TestPersistRejectsTriggerCollision(t *testing.T) {
	p, store := testPersister(t)
	manifest := `id: hotfix
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
	if err := store.Upsert(context.Background(), "sess-1", []byte(manifest), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	_, err := p.Persist(context.Background(), workflowcomposition.PersistRequest{
		SessionID:  "sess-1",
		ProjectDir: t.TempDir(),
		WorkflowID: "hotfix",
		Version:    "1.0.0",
		Confirm:    true,
		Trigger:    "/plan",
	})
	var vf *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	if len(vf.Errors) == 0 || vf.Errors[0].Code != "trigger_collision" {
		t.Fatalf("errors = %+v", vf.Errors)
	}
}

func TestPersistRejectsInvalidWorkflowID(t *testing.T) {
	p, _ := testPersister(t)
	_, err := p.Persist(context.Background(), workflowcomposition.PersistRequest{
		SessionID:  "sess-1",
		ProjectDir: t.TempDir(),
		WorkflowID: "Bad",
		Version:    "1.0.0",
		Confirm:    true,
	})
	var vf *workflowcomposition.ComposeValidationFailed
	if !errors.As(err, &vf) {
		t.Fatalf("err = %v", err)
	}
	if vf.Errors[0].Code != "invalid_workflow_id" {
		t.Fatalf("code = %q", vf.Errors[0].Code)
	}
}

func TestPersistWritesFileAndDeletesSessionRow(t *testing.T) {
	p, store := testPersister(t)
	projectDir := t.TempDir()
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
	if err := store.Upsert(context.Background(), "sess-1", []byte(manifest), workflowdrafts.Coordinator, nil); err != nil {
		testutil.FailErr(t, "store.Upsert failed", err)
	}
	result, err := p.Persist(context.Background(), workflowcomposition.PersistRequest{
		SessionID:  "sess-1",
		ProjectDir: projectDir,
		WorkflowID: "hotfix",
		Version:    "1.0.0",
		Confirm:    true,
		Trigger:    "/hotfix-custom",
	})
	testutil.FailErr(t, "p.Persist failed", err)
	if result.Path != settingsoverlay.DirName()+"/workflows/hotfix/workflow.yaml" {
		t.Fatalf("path = %q", result.Path)
	}
	if result.Summary.Scope != "project" {
		t.Fatalf("scope = %q", result.Summary.Scope)
	}
	if result.Summary.Trigger != "/hotfix-custom" {
		t.Fatalf("trigger = %q", result.Summary.Trigger)
	}
	data, err := os.ReadFile(filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "hotfix", "workflow.yaml"))
	testutil.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "trigger: /hotfix-custom") {
		t.Fatalf("yaml = %s", data)
	}
	if _, err := store.Get(context.Background(), "sess-1", "hotfix", "1.0.0"); err == nil {
		t.Fatal("expected session row deleted")
	}
}
