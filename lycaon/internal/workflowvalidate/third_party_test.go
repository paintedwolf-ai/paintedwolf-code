package workflowvalidate_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
)

const thirdPartyGolden = `id: custom-lite
version: 1.0.0
name: Custom lite
description: Minimal gate-kit custom workflow
trigger: custom-lite
request:
  question: What should this workflow investigate?
agents:
  - { id: implementer, tools: profile }
phases:
  - id: intake
    activity_label: Understanding the request
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [hitl_consulted:intake]
    advance:
      when_gate_met: coordinator
    next: done
  - id: done
    activity_label: Wrapping up
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    terminal: true
    complete_when: orchestration_complete
`

const thirdPartyDomainLeaf = `id: custom-bad
version: 1.0.0
name: Custom bad
description: Uses a bundled-only domain leaf
trigger: custom-bad
request:
  question: What should this workflow investigate?
agents:
  - { id: implementer, tools: profile }
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: gates_satisfied
    gates: [plan_stub_valid]
    next: ""
    terminal: true
`

func TestThirdPartyGateKitGoldenPasses(t *testing.T) {
	root := configlayout.FindModuleRoot()
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-lite.yaml")
	testutil.FailErr(t, "write golden", os.WriteFile(path, []byte(thirdPartyGolden), 0o644))

	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{path},
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s [%s] %s | %s", d.Field, d.Code, d.Message, d.Replacement)
		}
		t.Fatalf("golden custom workflow failed: %d diagnostics", len(diags))
	}
}

func TestThirdPartyDomainLeafFailsWithReplacement(t *testing.T) {
	root := configlayout.FindModuleRoot()
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-bad.yaml")
	testutil.FailErr(t, "write bad", os.WriteFile(path, []byte(thirdPartyDomainLeaf), 0o644))

	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{path},
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	found := false
	for _, d := range diags {
		if d.Code == string(workflowdiag.MustCode("domain_leaf_on_overlay")) {
			found = true
			if d.Replacement == "" {
				t.Fatal("expected non-empty replacement for domain_leaf_on_overlay")
			}
		}
	}
	if !found {
		for _, d := range diags {
			t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
		}
		t.Fatal("expected domain_leaf_on_overlay")
	}
}

const thirdPartyAttachSessionCreate = `id: custom-attach
version: 1.0.0
name: Custom Attach
description: Custom workflow trying session_create
trigger: manual
attach:
  policy: session_create
request:
  question: What to do?
phases:
  - id: only
    activity_label: Running
    coordinator_surface: await_user
    surface_template: agents/coordinator-surface-plan.md
    mode_refs: [await-user-input]
    complete_when: orchestration_complete
    terminal: true
`

func TestThirdPartyAttachSessionCreateFailsWithReplacement(t *testing.T) {
	root := configlayout.FindModuleRoot()
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-bad-attach.yaml")
	testutil.FailErr(t, "write bad", os.WriteFile(path, []byte(thirdPartyAttachSessionCreate), 0o644))

	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		Mode:       workflowvalidate.ModePaths,
		Paths:      []string{path},
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	found := false
	for _, d := range diags {
		if d.Code == string(workflowdiag.MustCode("attach_session_create_on_overlay")) {
			found = true
			if d.Replacement == "" {
				t.Fatal("expected non-empty replacement for attach_session_create_on_overlay")
			}
		}
	}
	if !found {
		for _, d := range diags {
			t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
		}
		t.Fatal("expected attach_session_create_on_overlay")
	}
}

// Project validation judges only project-scoped manifests.
func TestProjectModeJudgesOnlyOverlayManifests(t *testing.T) {
	root := configlayout.FindModuleRoot()
	project := t.TempDir()

	diags, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		ProjectDir: project,
		Mode:       workflowvalidate.ModeProject,
	})
	testutil.FailErr(t, "ValidateCatalog", err)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
		}
		t.Fatalf("project with no overlay produced %d diagnostics", len(diags))
	}

	overlayDir := filepath.Join(project, settingsoverlay.DirName(), "workflows", "custom-lite")
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(overlayDir, 0o750))
	testutil.FailErr(t, "write overlay",
		os.WriteFile(filepath.Join(overlayDir, "workflow.yaml"), []byte(thirdPartyGolden), 0o600))

	diags, err = workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		ProjectDir: project,
		Mode:       workflowvalidate.ModeProject,
	})
	testutil.FailErr(t, "ValidateCatalog overlay", err)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Logf("%s [%s] %s", d.Field, d.Code, d.Message)
		}
		t.Fatalf("valid overlay produced %d diagnostics", len(diags))
	}

	badDir := filepath.Join(project, settingsoverlay.DirName(), "workflows", "custom-bad")
	testutil.FailErr(t, "mkdir bad", os.MkdirAll(badDir, 0o750))
	testutil.FailErr(t, "write bad",
		os.WriteFile(filepath.Join(badDir, "workflow.yaml"), []byte(thirdPartyDomainLeaf), 0o600))

	diags, err = workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
		ConfigRoot: root,
		ProjectDir: project,
		Mode:       workflowvalidate.ModeProject,
	})
	testutil.FailErr(t, "ValidateCatalog bad overlay", err)
	for _, d := range diags {
		if !strings.Contains(d.Field, "custom-bad") {
			t.Fatalf("overlay rules reached a non-overlay manifest: %s [%s]", d.Field, d.Code)
		}
	}
	if len(diags) == 0 {
		t.Fatal("expected the author's own bad overlay to be reported")
	}
}
