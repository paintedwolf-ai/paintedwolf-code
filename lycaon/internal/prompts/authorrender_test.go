package prompts

import (
	"context"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorRender_BundledPartial(t *testing.T) {
	ResetPersonaContractCache()
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		TemplateRef: "partials/finish-handoff.md",
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if strings.TrimSpace(res.Output) == "" {
		t.Fatal("bundled partial rendered empty")
	}
}

func TestAuthorRender_PersonaHasRequiredHeadings(t *testing.T) {
	ResetPersonaContractCache()
	root := configlayout.FindModuleRoot()
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot: root,
		AgentID:    "implementer",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	contract, err := LoadPersonaContract()
	if err != nil {
		t.Fatalf("LoadPersonaContract: %v", err)
	}
	for _, heading := range contract.RequiredHeadingsRendered {
		if !strings.Contains(res.Output, heading) {
			t.Fatalf("stock persona is missing %q", heading)
		}
	}
	if len(res.Violations) != 0 {
		t.Fatalf("stock persona reported violations: %v", res.Violations)
	}
}

func TestAuthorRender_ProjectOverlayWins(t *testing.T) {
	ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "## Finish\n\nProject override body.\n"
	if err := os.WriteFile(filepath.Join(dir, "finish-handoff.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		ProjectDir:  project,
		TemplateRef: "partials/finish-handoff.md",
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "Project override body.") {
		t.Fatalf("overlay did not win:\n%s", res.Output)
	}
}

func TestPromptSnapshotCapturesOverrideBytesAndRevision(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir overlay: %v", err)
	}
	path := filepath.Join(dir, "finish-handoff.md")
	if err := os.WriteFile(path, []byte("first snapshot\n"), 0o600); err != nil {
		t.Fatalf("write first override: %v", err)
	}
	base := NewFileTemplateEngineLayers(PromptLayers{ModuleRoot: configlayout.FindModuleRoot()}).WithProjectOverlay(project)
	first, err := base.Snapshot()
	if err != nil {
		t.Fatalf("capture first snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("second snapshot\n"), 0o600); err != nil {
		t.Fatalf("write second override: %v", err)
	}
	second, err := base.Snapshot()
	if err != nil {
		t.Fatalf("capture second snapshot: %v", err)
	}
	if first.Revision() == second.Revision() {
		t.Fatal("override change must produce a new prompt revision")
	}
	firstOut, err := first.Render(context.Background(), "partials/finish-handoff.md", nil)
	if err != nil {
		t.Fatalf("render first snapshot: %v", err)
	}
	secondOut, err := second.Render(context.Background(), "partials/finish-handoff.md", nil)
	if err != nil {
		t.Fatalf("render second snapshot: %v", err)
	}
	if !strings.Contains(firstOut, "first snapshot") || !strings.Contains(secondOut, "second snapshot") {
		t.Fatalf("snapshots did not preserve their own bytes: first=%q second=%q", firstOut, secondOut)
	}
}

func TestAuthorRender_OverlayParseErrorFailsClosed(t *testing.T) {
	ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-handoff.md"), []byte("{% if unclosed %}\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	_, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		ProjectDir:  project,
		TemplateRef: "partials/finish-handoff.md",
	})
	if err == nil {
		t.Fatal("expected a parse error, got a render")
	}
	if !strings.Contains(err.Error(), "finish-handoff.md") {
		t.Fatalf("error does not name the template: %v", err)
	}
}

func TestAuthorRender_CheckCatchesStrippedHeadings(t *testing.T) {
	ResetPersonaContractCache()
	defer ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "agents")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "implementer.md"), []byte("## Grounding\n\nStripped.\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot: configlayout.FindModuleRoot(),
		ProjectDir: project,
		AgentID:    "implementer",
		Check:      true,
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if len(res.Violations) == 0 {
		t.Fatal("stripped override reported no violations")
	}
	var sawHeading bool
	for _, v := range res.Violations {
		if strings.HasPrefix(v, "missing_heading:") {
			sawHeading = true
		}
	}
	if !sawHeading {
		t.Fatalf("violations do not name a missing heading: %v", res.Violations)
	}
}

func TestAuthorRender_VarsReachTemplate(t *testing.T) {
	ResetPersonaContractCache()
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "prompt_files", "partials")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "finish-handoff.md"), []byte("focus={{ focus }}\n"), 0o600); err != nil {
		t.Fatalf("write override: %v", err)
	}
	res, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		ProjectDir:  project,
		TemplateRef: "partials/finish-handoff.md",
		Vars:        map[string]any{"focus": "triage"},
	})
	if err != nil {
		t.Fatalf("AuthorRender: %v", err)
	}
	if !strings.Contains(res.Output, "focus=triage") {
		t.Fatalf("--var did not reach the template:\n%s", res.Output)
	}
}

func TestAuthorRender_RefAndAgentAreExclusive(t *testing.T) {
	_, err := AuthorRender(context.Background(), AuthorRenderRequest{
		ModuleRoot:  configlayout.FindModuleRoot(),
		TemplateRef: "partials/finish-handoff.md",
		AgentID:     "implementer",
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}
