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

// Every declared preset has a boundary implementation.
func TestEveryPresetHasABoundary(t *testing.T) {
	t.Parallel()

	for _, preset := range contribution.PresetIDs() {
		if _, ok := editorturn.PresetBoundaryFor(preset); !ok {
			t.Errorf("preset %s has no host boundary implementation", preset)
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
