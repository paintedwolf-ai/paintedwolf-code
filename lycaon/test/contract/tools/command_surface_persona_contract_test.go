package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPathExplorerPersonaIncludesReadOnlyCommandSurface(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "path-explorer", nil)
	contractcheck.FailErr(t, "RenderPersona path-explorer", err)
	for _, forbid := range []string{"not a shell", "argv host runner", "COMMAND_NOT_ARGV", "external --", "Never `command`"} {
		if strings.Contains(got, forbid) {
			t.Fatalf("path-explorer persona must not include stale command fragment %q", forbid)
		}
	}
	for _, want := range []string{"## command", "GNU and BSD", "--help", "read-only"} {
		if !strings.Contains(got, want) {
			t.Fatalf("path-explorer persona missing read-only command surface %q", want)
		}
	}
}
