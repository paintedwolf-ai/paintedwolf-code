package prompts

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

const sealedSurvey = "security-survey/1.0.0"

func sealedGuidance(t *testing.T, eff *extpacks.EffectiveCatalog, stem string) []byte {
	t.Helper()
	data, _, ok := eff.UnitContent(extpacks.ArchiveGuidanceUnitID(sealedSurvey, stem))
	if !ok {
		t.Fatalf("stock catalog does not seal %s", stem)
	}
	return data
}

// A run on a sealed version reads its own phase guidance ahead of project
// overrides; refs the archive does not seal fall through to the live layers.
func TestWorkflowArchiveLayerPrecedesProjectAndBundledGuidance(t *testing.T) {
	eff := extpackstest.StockCatalog(t)
	project := t.TempDir()
	testutil.FailErr(t, "mkdir override", os.MkdirAll(filepath.Join(project, "guidance"), 0o755))
	testutil.FailErr(t, "write override", os.WriteFile(filepath.Join(project, "guidance", "coordinator-security-challenge.md"), []byte("project override\n"), 0o600))
	live := PromptLayers{Catalog: eff, ProjectActive: project}
	sealed := live
	sealed.WorkflowArchive = sealedSurvey

	got, err := sealed.ReadFile("guidance/coordinator-security-challenge.md")
	testutil.FailErr(t, "read sealed guidance", err)
	if !bytes.Equal(got, sealedGuidance(t, eff, "coordinator-security-challenge")) {
		t.Fatalf("archive layer did not serve the sealed bytes: %q", got)
	}
	overridden, err := live.ReadFile("guidance/coordinator-security-challenge.md")
	testutil.FailErr(t, "read live guidance", err)
	if string(overridden) != "project override\n" {
		t.Fatalf("live run lost its project override: %q", overridden)
	}

	unsealed, err := sealed.ReadFile("guidance/tool-procedures.md")
	testutil.FailErr(t, "read unsealed guidance", err)
	bundled, err := PromptLayers{Catalog: eff}.ReadFile("guidance/tool-procedures.md")
	testutil.FailErr(t, "read bundled guidance", err)
	if !bytes.Equal(unsealed, bundled) {
		t.Fatal("guidance the archive does not seal must resolve from the live layers")
	}
}

// Compiled templates are cached by revision, so two runs on different
// versions of one workflow must never share a revision.
func TestWorkflowArchiveSeparatesPromptRevisions(t *testing.T) {
	eff := extpackstest.StockCatalog(t)
	base := NewFileTemplateEngineLayers(PromptLayers{Catalog: eff})
	if base.WithWorkflowArchive("") != base {
		t.Fatal("an empty archive key must leave the engine unchanged")
	}
	live, err := base.Snapshot()
	testutil.FailErr(t, "snapshot live", err)
	sealed, err := base.WithWorkflowArchive(sealedSurvey).Snapshot()
	testutil.FailErr(t, "snapshot sealed", err)
	if live.Revision() == sealed.Revision() {
		t.Fatal("live and sealed engines share a prompt revision")
	}
	rendered, err := sealed.Render(t.Context(), "guidance/coordinator-security-plan.md", map[string]any{})
	testutil.FailErr(t, "render sealed guidance", err)
	current, err := live.Render(t.Context(), "guidance/coordinator-security-plan.md", map[string]any{})
	testutil.FailErr(t, "render live guidance", err)
	if rendered == current {
		t.Fatal("sealed snapshot rendered the live guidance")
	}
}
