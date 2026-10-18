package inject_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"strings"
	"testing"
)

func TestProgressRecoveryUsesCatalogBinding(t *testing.T) {
	block, err := inject.RenderProgressRecoveryBlock(t.Context(), promptstest.InjectRenderer(t), "repair")
	testutil.FailErr(t, "render repair", err)
	for _, text := range []string{"earlier rejected calls", "If those operations are still required", "normal tool admission", "before moving on"} {
		if !strings.Contains(block, text) {
			t.Fatalf("missing %q: %s", text, block)
		}
	}
}
