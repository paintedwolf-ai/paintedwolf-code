package contract

import (
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestParseManifestRejectsInvalidSetPosture(t *testing.T) {
	t.Parallel()
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: research
    on_enter:
      set_posture: research
`))
	if err == nil {
		t.Fatal("expected parse failure for phase id as set_posture")
	}
}
