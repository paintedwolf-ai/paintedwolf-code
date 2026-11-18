package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/editorturn"
)

// Every declared preset has a boundary implementation.
func TestEveryPresetHasABoundary(t *testing.T) {
	t.Parallel()

	ids := contribution.PresetIDs()
	if len(ids) == 0 {
		t.Fatal("no editor presets declared")
	}
	for _, preset := range ids {
		if _, ok := editorturn.PresetBoundaryFor(preset); !ok {
			t.Errorf("preset %s has no host boundary implementation", preset)
		}
	}
}

// Stock editor actions name prompt units that the invoke path finds in the
// captured frame.
func TestStockEditorActionPromptsResolve(t *testing.T) {
	t.Parallel()
	view := stockCatalogView(t)
	frame := &contribframe.Frame{View: view}

	actions := view.Contributions.EditorActions()
	if len(actions) == 0 {
		t.Fatal("no stock editor actions compiled")
	}
	for _, action := range actions {
		ref := strings.TrimSpace(action.Execution.PromptRef)
		if ref == "" {
			t.Errorf("editor action %s declares no prompt_ref", action.ID)
			continue
		}
		if len(frame.PromptBody(ref)) == 0 {
			t.Errorf("editor action %s names prompt %s, which the stock catalog does not load", action.ID, ref)
		}
	}
}
