package guidance

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts/promptstest"
)

// setTestRenderer wires bundled guidance templates for this package's tests.
func setTestRenderer(t *testing.T) {
	t.Helper()
	SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
}
