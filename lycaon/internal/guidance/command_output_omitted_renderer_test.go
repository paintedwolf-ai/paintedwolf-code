package guidance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolreach"
)

func siteOverlayRenderer(t *testing.T, body string) *prompts.GuidanceRenderer {
	t.Helper()
	site := t.TempDir()
	path := filepath.Join(site, "guidance", filepath.FromSlash(UtilityCommandOutputOmittedRef)+".md")
	testutil.FailErr(t, "create overlay directory", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write overlay", os.WriteFile(path, []byte(body), 0o644))
	return prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{Site: site}))
}

func renderOmittedNotice(t *testing.T) (string, error) {
	t.Helper()
	reach := promptstest.StockToolReach(t, toolreach.ProgramTool)
	return RenderCommandOutputOmitted(t.Context(), reach, "10", "1 KB", "@output/prog.stdout", 2, 3, 20, 400, true)
}

func TestRenderCommandOutputOmittedUsesTheConfiguredRenderer(t *testing.T) {
	t.Cleanup(func() { SetGuidanceRenderer(nil) })

	SetGuidanceRenderer(nil)
	if _, err := renderOmittedNotice(t); err == nil {
		t.Fatal("an unconfigured renderer produced a notice")
	}

	SetGuidanceRenderer(siteOverlayRenderer(t, "overlay {{ next_line }}"))
	got, err := renderOmittedNotice(t)
	testutil.FailErr(t, "render overlay", err)
	if got != "overlay 2" {
		t.Fatalf("notice = %q, want the overlay's render", got)
	}

	SetGuidanceRenderer(siteOverlayRenderer(t, "{% if %}"))
	if _, err := renderOmittedNotice(t); err == nil {
		t.Fatal("a broken overlay was replaced by a stock render")
	}

	SetGuidanceRenderer(siteOverlayRenderer(t, "  "))
	if _, err := renderOmittedNotice(t); err == nil {
		t.Fatal("an empty notice was accepted")
	}
}
