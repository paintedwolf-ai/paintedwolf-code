package profiles

import (
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
)

func TestPromptSurfaceFingerprintTracksSkillAvailability(t *testing.T) {
	first := prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "first", Description: "First procedure"}}}
	second := prompts.AgentPromptSurface{Skills: []prompts.AgentSkillView{{Name: "second", Description: "Second procedure"}}}
	if promptSurfaceFingerprint(first) != promptSurfaceFingerprint(second) {
		t.Fatal("skill catalog changes that do not alter prompt text should keep the prefix")
	}
	if promptSurfaceFingerprint(first) == promptSurfaceFingerprint(prompts.AgentPromptSurface{}) {
		t.Fatal("skill availability changes the prompt procedure")
	}
}
