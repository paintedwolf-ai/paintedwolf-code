package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The prompt renderer passes every template the variables its caller assembled,
// selecting neither by template ref nor by key; a dropped variable renders as a
// silent empty branch. Each entry point renders a template that prints a key no
// reader declares.
func TestPromptRendererPassesVarsUnfiltered(t *testing.T) {
	t.Parallel()
	const body = "{{ unfiltered_probe }}{% if unfiltered_flag %}+flag{% endif %}"
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: filepath.Join(contractcheck.RepoRoot(t), "lycaon")})
	for _, ref := range []string{
		"agents/unfiltered-probe.md",
		"partials/unfiltered-probe.md",
		"units/unfiltered-probe.md",
		prompts.InjectTemplatePrefix + "unfiltered-probe.md",
		prompts.GuidanceTemplatePrefix + "unfiltered-probe.md",
		prompts.KicksTemplatePrefix + "unfiltered-probe.md",
	} {
		contractcheck.FailErr(t, "register "+ref, engine.Register(ref, body))
	}
	ctx := context.Background()
	data := func() map[string]any {
		return map[string]any{"unfiltered_probe": "probe-value-7f3a", "unfiltered_flag": true}
	}
	const want = "probe-value-7f3a+flag"
	renders := map[string]func() (string, error){
		"agents":   func() (string, error) { return engine.Render(ctx, "agents/unfiltered-probe.md", data()) },
		"partials": func() (string, error) { return engine.Render(ctx, "partials/unfiltered-probe.md", data()) },
		"units":    func() (string, error) { return engine.RenderUnit(ctx, "units/unfiltered-probe.md", data()) },
		"inject":   func() (string, error) { return engine.RenderInject(ctx, "unfiltered-probe", data()) },
		"guidance": func() (string, error) { return engine.RenderGuidance(ctx, "unfiltered-probe", data()) },
		"kicks":    func() (string, error) { return engine.RenderKick(ctx, "unfiltered-probe", data()) },
	}
	for name, render := range renders {
		got, err := render()
		contractcheck.FailErr(t, "render "+name, err)
		if got != want {
			t.Errorf("%s render = %q, want %q", name, got, want)
		}
	}
}
