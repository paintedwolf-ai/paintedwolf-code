package presentation_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
)

func TestProjectPhaseExitListsChoiceArms(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "fixtures", "workflows", "choice-transitions.yaml")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read fixture", err)
	m, err := workflowdef.ParseManifestYAML(raw)
	testutil.FailErr(t, "ParseManifestYAML", err)
	m = workflowdef.FinalizeManifest(m)
	def, ok := m.PhaseByID("decide")
	if !ok {
		t.Fatal("missing decide")
	}
	exit := workflowpresentation.ProjectPhaseExit(m, def, nil, nil)
	if len(exit.ChoiceTransitions) != 3 {
		t.Fatalf("ChoiceTransitions len = %d want 3", len(exit.ChoiceTransitions))
	}
	var ids []string
	for _, arm := range exit.ChoiceTransitions {
		ids = append(ids, arm.ID)
	}
	joined := strings.Join(ids, ",")
	for _, want := range []string{"critique", "deepen", "side_quest"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("choice arms missing %q: %s", want, joined)
		}
	}
}
