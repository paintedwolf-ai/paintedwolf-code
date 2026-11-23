package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/editorturn"
)

// Every shipped action uses a registered boundary.
func TestEveryPresetHasABoundary(t *testing.T) {
	t.Parallel()

	used := map[contribution.PresetID]bool{}
	for _, action := range stockCatalogView(t).Contributions.EditorActions() {
		preset := action.Execution.Preset
		boundary, ok := editorturn.PresetBoundaryFor(preset)
		if !ok || boundary.ToolProfile == "" {
			t.Errorf("action %s has no usable boundary for preset %s", action.ID, preset)
		}
		used[preset] = true
	}
	for _, preset := range []contribution.PresetID{
		contribution.PresetInspectFile, contribution.PresetEditFile,
		contribution.PresetEditSibling, contribution.PresetFixFinding,
	} {
		if !used[preset] {
			t.Errorf("preset %s has no shipped action", preset)
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
