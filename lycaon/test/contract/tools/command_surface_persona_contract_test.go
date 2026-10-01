package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPathExplorerPersonaExcludesCommandSurface(t *testing.T) {
	contractcheck.ActivateStockCatalog(t)
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "path-explorer", nil)
	contractcheck.FailErr(t, "RenderPersona path-explorer", err)
	for _, forbid := range []string{"## command", "not a shell", "argv host runner", "`rg *`", "`tree *`", "`find *`", "COMMAND_NOT_ARGV", "COMMAND_ARGV_REQUIRED"} {
		if strings.Contains(got, forbid) {
			t.Fatalf("path-explorer persona must not include command surface fragment %q", forbid)
		}
	}
	for _, want := range []string{"Never `command`", "`find`", "`list_dir`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("path-explorer persona missing native survey hint %q", want)
		}
	}
}
