package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

// stockBlockPlane mirrors production policy rendering.
func stockBlockPlane(t *testing.T) *toolfeedback.BlockPlane {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")
	repoRoot := filepath.Join(moduleRoot, "..")
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallFile(
		filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))

	l, err := oar.NewLoader(filepath.Join(repoRoot, "schemas"))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock", err)
	pipeline := oar.NewGuardPipeline(rs, l, oar.NewCounterStore())
	// Enable both anchors used by tool rejections.
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)

	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return &toolfeedback.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(guidance.NewStaticRejectFormatter(cfg), nil)}
}
