package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/editorturn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every shipped action uses a registered boundary.
func TestEveryPresetHasABoundary(t *testing.T) {
	t.Parallel()

	used := map[contribution.PresetID]bool{}
	for _, action := range stockContributionSet(t).EditorActions() {
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

// Stock editor actions reference bundled prompts.
func TestStockEditorActionPromptsResolve(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	set := stockContributionSet(t)

	actions := set.EditorActions()
	if len(actions) == 0 {
		t.Fatal("no stock editor actions compiled")
	}
	for _, action := range actions {
		ref := strings.TrimSpace(action.Execution.PromptRef)
		if ref == "" {
			t.Errorf("editor action %s declares no prompt_ref", action.ID)
			continue
		}
		path := filepath.Join(root, "lycaon", "config", "packs",
			"painted-wolf", "platform", filepath.FromSlash(ref)+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("editor action %s names prompt %s, which no stock unit provides",
				action.ID, ref)
		}
	}
}
