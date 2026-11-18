package prompts_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	overlayProbeRef  = "agents/overlay-probe.md"
	overlayProbeLeaf = "overlay-probe.md"
)

func stageBundledProbe(t *testing.T, body string) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.PlatformPrompts.Join(overlayProbeLeaf): body,
	})
}

func writeOverlayProbe(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", overlayProbeLeaf), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
}

func renderProbe(t *testing.T, layers prompts.PromptLayers) string {
	t.Helper()
	got, err := prompts.NewFileTemplateEngineLayers(layers).Render(context.Background(), overlayProbeRef, nil)
	testutil.FailErr(t, "render prompt template", err)
	return got
}

func assertLayerWins(t *testing.T, got, want, shadowed string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("got %q want %q to win", got, want)
	}
	if strings.Contains(got, shadowed) {
		t.Fatalf("got %q still carries shadowed body %q", got, shadowed)
	}
}

func TestPromptFilesOverlayWins(t *testing.T) {
	stageBundledProbe(t, "bundled-marker")
	site := t.TempDir()
	writeOverlayProbe(t, site, "site-overlay-wins")

	if got := renderProbe(t, prompts.PromptLayers{}); !strings.Contains(got, "bundled-marker") {
		t.Fatalf("bundled layer = %q want bundled-marker", got)
	}
	assertLayerWins(t, renderProbe(t, prompts.PromptLayers{Site: site}), "site-overlay-wins", "bundled-marker")
}

func TestOverlayCannotDropRequiredPartial(t *testing.T) {
	site := t.TempDir()
	testutil.FailErr(t, "create directory", os.MkdirAll(filepath.Join(site, "partials"), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(site, "partials", "coordination-loop.md"), []byte("\n"), 0o644))
	reader, err := prompts.DeclaredReader(nil, "implement")
	testutil.FailErr(t, "reader", err)
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: site})
	if _, err := prompts.RenderPersona(context.Background(), engine, "implementer", reader, nil); err == nil || !strings.Contains(err.Error(), "coordination-loop.md") {
		t.Fatalf("a persona rendered over an emptied required partial: err = %v", err)
	}
}

func TestProjectOverlayWinsOverBundled(t *testing.T) {
	stageBundledProbe(t, "bundled-marker")
	project := t.TempDir()
	writeOverlayProbe(t, prompts.ProjectPromptFilesDir(project), "project-wins")

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}).WithProjectOverlay(project)
	got, err := engine.Render(context.Background(), overlayProbeRef, nil)
	testutil.FailErr(t, "render prompt template", err)
	assertLayerWins(t, got, "project-wins", "bundled-marker")
}

func TestOverlayPathHelpers(t *testing.T) {
	root := t.TempDir()
	// Temp dirs are not checkouts — Site overlay is checkout-only.
	if prompts.SitePromptFilesDir(root) != "" {
		t.Fatalf("SitePromptFilesDir(temp) = %q, want empty", prompts.SitePromptFilesDir(root))
	}
	if got := prompts.ProjectPromptFilesDir(root); !strings.Contains(got, settingsoverlay.DirName()) {
		t.Fatalf("ProjectPromptFilesDir = %q", got)
	}
	if prompts.ProjectPromptFilesDir("") != "" {
		t.Fatal("empty project dir")
	}
	if prompts.SitePromptFilesDir("") != "" {
		t.Fatal("empty config root")
	}
	if prompts.SitePromptFilesDir("lycaon") != "" {
		t.Fatal("ghost FindModuleRoot must not invent a site overlay path")
	}
}

func TestFileTemplateEngineRegisterValidation(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("", "x"); err == nil {
		t.Fatal("expected empty name error")
	}
}

func TestFileTemplateEngineLayersMetadata(t *testing.T) {
	mod := configlayout.FindModuleRoot()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: mod})
	if engine.ModuleRoot() != mod {
		t.Fatalf("ModuleRoot = %q want %q", engine.ModuleRoot(), mod)
	}
	if engine.Layers().ModuleRoot != mod {
		t.Fatal("Layers().ModuleRoot mismatch")
	}
	project := t.TempDir()
	derived := engine.WithProjectOverlay(project)
	if got := derived.Layers().ProjectPrimary; got != prompts.ProjectPromptFilesDir(project) {
		t.Fatalf("derived ProjectPrimary layer = %q want %q", got, prompts.ProjectPromptFilesDir(project))
	}
	if derived.ModuleRoot() != mod {
		t.Fatalf("derived ModuleRoot = %q want %q", derived.ModuleRoot(), mod)
	}
	if engine.Layers().ProjectPrimary != "" {
		t.Fatalf("base engine ProjectPrimary layer mutated: %q", engine.Layers().ProjectPrimary)
	}
}

func TestWithProjectOverlayReplacesActiveLayer(t *testing.T) {
	primary := t.TempDir()
	active := t.TempDir()
	replacement := t.TempDir()
	base := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}).
		WithProjectOverlays([]string{primary, active})
	derived := base.WithProjectOverlay(replacement)
	if got := derived.Layers().ProjectPrimary; got != prompts.ProjectPromptFilesDir(replacement) {
		t.Fatalf("ProjectPrimary = %q", got)
	}
	if got := derived.Layers().ProjectActive; got != "" {
		t.Fatalf("ProjectActive = %q, want empty", got)
	}
	if got := base.Layers().ProjectActive; got != prompts.ProjectPromptFilesDir(active) {
		t.Fatalf("base ProjectActive = %q", got)
	}
}

func TestProjectActiveOverlayWinsOverPrimary(t *testing.T) {
	stageBundledProbe(t, "bundled-marker")
	primary := t.TempDir()
	active := t.TempDir()
	writeOverlayProbe(t, prompts.ProjectPromptFilesDir(primary), "primary-layer")
	writeOverlayProbe(t, prompts.ProjectPromptFilesDir(active), "active-wins")

	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{}).
		WithProjectOverlays([]string{primary, active})
	got, err := engine.Render(context.Background(), overlayProbeRef, nil)
	testutil.FailErr(t, "render prompt template", err)
	assertLayerWins(t, got, "active-wins", "primary-layer")
	if strings.Contains(got, "bundled-marker") {
		t.Fatalf("got %q still carries the bundled body", got)
	}
}
