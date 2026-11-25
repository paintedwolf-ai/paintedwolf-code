package command

import (
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
)

// TestMain installs the stock guidance renderer the host installs at startup.
func TestMain(m *testing.M) {
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	os.Exit(m.Run())
}
